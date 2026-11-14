// Package pgwire provides a deliberately narrow PostgreSQL v3.0 ingress.
// It preserves one upstream session and the original authentication identity.
package pgwire

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"github.com/jackc/pgx/v5/pgproto3"
	"io"
	"net"
	"pooltune/internal/domain"
	"pooltune/internal/observation"
	"pooltune/internal/scheduler"
	"sync"
	"sync/atomic"
	"time"
)

type TargetConfig struct {
	// PreserveReadIdle leaves established reads open; bounded sessions and write deadlines still apply.
	PreserveReadIdle                            bool
	AllowedUser                                 string
	Ready                                       func() bool
	ClassifySession                             func(authenticatedUser, database, sessionID string) domain.Classification
	Observe                                     func(Observation)
	Target, Database, Upstream                  string
	ServerTLS, UpstreamTLS                      *tls.Config
	LocalTest                                   bool // plaintext/trust allowed only on literal loopback endpoints
	MaxSessions, MaxHandshakes, MaxMessageBytes int
	HandshakeTimeout, IOTimeout                 time.Duration
	Classify                                    func(authenticatedUser, database string) domain.Classification
}
type Server struct {
	nextObservation      atomic.Uint64
	cfg                  TargetConfig
	scheduler            *scheduler.Scheduler
	mu                   sync.Mutex
	connections          map[net.Conn]struct{}
	cancels              map[[8]byte]*session
	uncertain            map[*scheduler.Permit]struct{}
	sessions, handshakes chan struct{}
	serving              bool
	wg                   sync.WaitGroup
}
type session struct {
	ready            bool
	server           *Server
	cancel           context.CancelFunc
	client, upstream net.Conn
	key              [8]byte
	backendPID       uint32
	backendKey       []byte
}

func NewServer(cfg TargetConfig, s *scheduler.Scheduler) (*Server, error) {
	host, _, err := net.SplitHostPort(cfg.Upstream)
	if err != nil || s == nil || cfg.Target == "" || cfg.Database == "" || (cfg.Classify == nil && cfg.ClassifySession == nil) || cfg.MaxSessions <= 0 || cfg.MaxSessions > 10000 || cfg.MaxHandshakes <= 0 || cfg.MaxHandshakes > 1000 || cfg.MaxMessageBytes < 1024 || cfg.MaxMessageBytes > 16<<20 || cfg.HandshakeTimeout <= 0 || cfg.IOTimeout <= 0 {
		return nil, errors.New("invalid PostgreSQL ingress configuration")
	}
	target, path := s.Identity()
	if target != cfg.Target || path != domain.PathPostgres {
		return nil, errors.New("scheduler target/path mismatch")
	}
	if cfg.LocalTest {
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return nil, errors.New("test profile requires literal loopback")
		}
	}
	if !cfg.LocalTest || cfg.ServerTLS != nil || cfg.UpstreamTLS != nil {
		if cfg.ServerTLS == nil || cfg.UpstreamTLS == nil || cfg.UpstreamTLS.InsecureSkipVerify || cfg.UpstreamTLS.ServerName == "" || len(cfg.ServerTLS.Certificates) == 0 {
			return nil, errors.New("verified TLS required")
		}
		cfg.ServerTLS = cfg.ServerTLS.Clone()
		cfg.UpstreamTLS = cfg.UpstreamTLS.Clone()
		cfg.ServerTLS.MinVersion = max(tls.VersionTLS12, cfg.ServerTLS.MinVersion)
		cfg.UpstreamTLS.MinVersion = max(tls.VersionTLS12, cfg.UpstreamTLS.MinVersion)
	}
	return &Server{cfg: cfg, scheduler: s, connections: make(map[net.Conn]struct{}), cancels: make(map[[8]byte]*session), uncertain: make(map[*scheduler.Permit]struct{}), sessions: make(chan struct{}, cfg.MaxSessions), handshakes: make(chan struct{}, cfg.MaxHandshakes)}, nil
}

func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	if ctx == nil || l == nil {
		return errors.New("missing context/listener")
	}
	if s.cfg.LocalTest {
		h, _, e := net.SplitHostPort(l.Addr().String())
		ip := net.ParseIP(h)
		if e != nil || ip == nil || !ip.IsLoopback() {
			return errors.New("test listener must be loopback")
		}
	}
	s.mu.Lock()
	if s.serving {
		s.mu.Unlock()
		return errors.New("server already running")
	}
	s.serving = true
	s.mu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	stop := func() {
		l.Close()
		s.mu.Lock()
		for c := range s.connections {
			c.Close()
		}
		s.mu.Unlock()
	}
	done := make(chan struct{})
	defer func() { cancel(); stop(); s.wg.Wait(); close(done) }()
	go func() {
		select {
		case <-ctx.Done():
			stop()
		case <-done:
		}
	}()
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case s.handshakes <- struct{}{}:
		default:
			c.Close()
			continue
		}
		s.mu.Lock()
		if ctx.Err() != nil {
			s.mu.Unlock()
			<-s.handshakes
			c.Close()
			return nil
		}
		s.connections[c] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer c.Close()
			defer func() { s.mu.Lock(); delete(s.connections, c); s.mu.Unlock() }()
			s.handle(ctx, c)
		}()
	}
}
func (s *Server) UncertainCount() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.uncertain) }

func (s *Server) handle(parent context.Context, raw net.Conn) {
	handshakeHeld := true
	defer func() {
		if handshakeHeld {
			<-s.handshakes
		}
	}()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	raw.SetDeadline(time.Now().Add(s.cfg.HandshakeTimeout))
	client := raw
	b := pgproto3.NewBackend(client, client)
	b.SetMaxBodyLen(s.cfg.MaxMessageBytes)
	first, err := b.ReceiveStartupMessage()
	if err != nil {
		return
	}
	if _, ok := first.(*pgproto3.SSLRequest); ok {
		if s.cfg.ServerTLS == nil {
			writeAll(client, []byte{'N'})
			first, err = b.ReceiveStartupMessage()
		} else {
			if writeAll(client, []byte{'S'}) != nil {
				return
			}
			tc := tls.Server(raw, s.cfg.ServerTLS)
			if tc.HandshakeContext(ctx) != nil {
				return
			}
			client = tc
			b = pgproto3.NewBackend(client, client)
			b.SetMaxBodyLen(s.cfg.MaxMessageBytes)
			first, err = b.ReceiveStartupMessage()
		}
	} else if s.cfg.ServerTLS != nil {
		if _, ok := first.(*pgproto3.CancelRequest); !ok {
			sendError(client, "08004", "TLS required")
			return
		}
	}
	if err != nil {
		return
	}
	if c, ok := first.(*pgproto3.CancelRequest); ok {
		s.cancelRequest(ctx, c)
		return
	}
	st, ok := first.(*pgproto3.StartupMessage)
	if !ok || st.ProtocolVersion != pgproto3.ProtocolVersion30 || !s.validStartup(st) {
		sendError(client, "0A000", "unsupported startup profile")
		return
	}
	if s.cfg.AllowedUser != "" && st.Parameters["user"] != s.cfg.AllowedUser {
		sendError(client, "28000", "unsupported pool identity")
		return
	}
	if s.cfg.Ready != nil && !s.cfg.Ready() {
		sendError(client, "57P03", "recovery required")
		return
	}
	keepSlot := false
	select {
	case s.sessions <- struct{}{}:
		defer func() {
			if !keepSlot {
				<-s.sessions
			}
		}()
	default:
		sendError(client, "53300", "session capacity exhausted")
		return
	}
	up, err := s.dial(ctx)
	if err != nil {
		sendError(client, "08006", "upstream connection failed")
		return
	}
	defer up.Close()
	ss := &session{server: s, cancel: cancel, client: client, upstream: up}
	defer func() { s.mu.Lock(); delete(s.cancels, ss.key); s.mu.Unlock() }()
	f := pgproto3.NewFrontend(up, up)
	f.SetMaxBodyLen(s.cfg.MaxMessageBytes)
	f.Send(st)
	if f.Flush() != nil {
		return
	}
	if err = s.authenticate(ctx, ss, b, f); err != nil {
		sendError(client, "08004", "unsupported or failed authentication profile")
		return
	}
	client.SetDeadline(time.Time{})
	up.SetDeadline(time.Time{})
	<-s.handshakes
	handshakeHeld = false
	keepSlot = s.relay(ctx, ss, b, f, st.Parameters["user"])
}

func (s *Server) validStartup(st *pgproto3.StartupMessage) bool {
	if st.Parameters["database"] != s.cfg.Database || st.Parameters["user"] == "" || len(st.Parameters["user"]) > 128 {
		return false
	}
	for k, v := range st.Parameters {
		if len(v) > 256 {
			return false
		}
		switch k {
		case "user", "database", "application_name", "client_encoding", "DateStyle", "TimeZone", "extra_float_digits":
		default:
			return false
		}
	}
	return true
}
func (s *Server) dial(ctx context.Context) (net.Conn, error) {
	dialer := net.Dialer{Timeout: s.cfg.HandshakeTimeout}
	c, err := dialer.DialContext(ctx, "tcp", s.cfg.Upstream)
	if err != nil {
		return nil, err
	}
	c.SetDeadline(time.Now().Add(s.cfg.HandshakeTimeout))
	if s.cfg.UpstreamTLS == nil {
		return c, nil
	}
	packet, _ := (&pgproto3.SSLRequest{}).Encode(nil)
	if err = writeAll(c, packet); err != nil {
		c.Close()
		return nil, err
	}
	var reply [1]byte
	if _, err = io.ReadFull(c, reply[:]); err != nil || reply[0] != 'S' {
		c.Close()
		return nil, errors.New("upstream TLS unavailable")
	}
	tc := tls.Client(c, s.cfg.UpstreamTLS)
	if err = tc.HandshakeContext(ctx); err != nil {
		c.Close()
		return nil, err
	}
	return tc, nil
}
func (s *Server) authenticate(ctx context.Context, ss *session, b *pgproto3.Backend, f *pgproto3.Frontend) error {
	authenticated, final := false, false
	stage := 0
	upstreamBinding := false
	for i := 0; i < 128; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		m, err := f.Receive()
		if err != nil {
			return err
		}
		switch v := m.(type) {
		case *pgproto3.AuthenticationSASL:
			if stage != 0 {
				return ErrUnsupported
			}
			available := false
			for _, mechanism := range v.AuthMechanisms {
				if mechanism == "SCRAM-SHA-256-PLUS" {
					upstreamBinding = true
				}
				if mechanism == "SCRAM-SHA-256" {
					available = true
				}
			}
			if !available {
				return ErrUnsupported
			}
			b.Send(&pgproto3.AuthenticationSASL{AuthMechanisms: []string{"SCRAM-SHA-256"}})
			if b.Flush() != nil {
				return io.ErrClosedPipe
			}
			b.SetAuthType(pgproto3.AuthTypeSASL)
			response, err := b.Receive()
			if err != nil {
				return err
			}
			r, ok := response.(*pgproto3.SASLInitialResponse)
			if !ok || r.AuthMechanism != "SCRAM-SHA-256" || !nonBindingSCRAMInitial(r.Data, upstreamBinding) {
				return ErrUnsupported
			}
			f.Send(r)
			if f.Flush() != nil {
				return io.ErrClosedPipe
			}
			stage = 1
			continue
		case *pgproto3.AuthenticationSASLContinue:
			if stage != 1 {
				return ErrUnsupported
			}
			b.Send(v)
			if b.Flush() != nil {
				return io.ErrClosedPipe
			}
			b.SetAuthType(pgproto3.AuthTypeSASLContinue)
			response, err := b.Receive()
			if err != nil {
				return err
			}
			r, ok := response.(*pgproto3.SASLResponse)
			if !ok {
				return ErrUnsupported
			}
			f.Send(r)
			if f.Flush() != nil {
				return io.ErrClosedPipe
			}
			stage = 2
			continue
		case *pgproto3.AuthenticationSASLFinal:
			if stage != 2 {
				return ErrUnsupported
			}
			final = true
		case *pgproto3.AuthenticationOk:
			if !final && !s.cfg.LocalTest {
				return ErrUnsupported
			}
			authenticated = true
		case *pgproto3.BackendKeyData:
			if !authenticated || len(v.SecretKey) != 4 || ss.backendKey != nil {
				return ErrUnsupported
			}
			ss.backendPID = v.ProcessID
			ss.backendKey = append([]byte(nil), v.SecretKey...)
			s.mu.Lock()
			for {
				if _, err = rand.Read(ss.key[:]); err != nil {
					s.mu.Unlock()
					return err
				}
				if _, exists := s.cancels[ss.key]; !exists {
					break
				}
			}
			s.cancels[ss.key] = ss
			s.mu.Unlock()
			b.Send(&pgproto3.BackendKeyData{ProcessID: binary.BigEndian.Uint32(ss.key[:4]), SecretKey: append([]byte(nil), ss.key[4:]...)})
			if b.Flush() != nil {
				return io.ErrClosedPipe
			}
			continue
		case *pgproto3.ReadyForQuery:
			if !authenticated || v.TxStatus != 'I' || ss.backendKey == nil {
				return ErrUnsupported
			}
			b.Send(v)
			if err := b.Flush(); err != nil {
				return err
			}
			s.mu.Lock()
			ss.ready = true
			s.mu.Unlock()
			return nil
		case *pgproto3.ParameterStatus, *pgproto3.NoticeResponse:
			if !authenticated {
				return ErrUnsupported
			}
		case *pgproto3.ErrorResponse:
			b.Send(v)
			b.Flush()
			return errors.New("upstream authentication rejected")
		default:
			return ErrUnsupported
		}
		b.Send(m)
		if b.Flush() != nil {
			return io.ErrClosedPipe
		}
	}
	return errors.New("handshake message limit")
}

type wireEvent struct {
	data        []byte
	kind, ready byte
	err         error
}

func (s *Server) relay(ctx context.Context, ss *session, b *pgproto3.Backend, f *pgproto3.Frontend, user string) (uncertain bool) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	front, back := make(chan wireEvent, 1), make(chan wireEvent, 1)
	var readers sync.WaitGroup
	readers.Add(2)
	defer func() { cancel(); ss.client.Close(); ss.upstream.Close(); readers.Wait() }()
	go func() {
		defer readers.Done()
		for {
			if !s.cfg.PreserveReadIdle {
				ss.client.SetReadDeadline(time.Now().Add(s.cfg.IOTimeout))
			}
			m, e := b.Receive()
			event := wireEvent{err: e}
			if e == nil {
				event.data, e = m.Encode(nil)
				event.err = e
			}
			if e != nil {
				cancel()
				return
			}
			select {
			case front <- event:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		defer readers.Done()
		for {
			if !s.cfg.PreserveReadIdle {
				ss.upstream.SetReadDeadline(time.Now().Add(s.cfg.IOTimeout))
			}
			m, e := f.Receive()
			event := wireEvent{err: e}
			if e == nil {
				event.data, e = m.Encode(nil)
				event.err = e
				if e == nil && len(event.data) > 0 {
					event.kind = event.data[0]
				}
				if r, ok := m.(*pgproto3.ReadyForQuery); ok {
					event.ready = r.TxStatus
				}
			}
			select {
			case back <- event:
			case <-ctx.Done():
				return
			}
			if e != nil {
				return
			}
		}
	}()
	sessionID := make([]byte, 16)
	if _, err := rand.Read(sessionID); err != nil {
		return false
	}
	identity := hex.EncodeToString(sessionID)
	var observed Observation
	var phase flow
	var permit *scheduler.Permit
	defer func() {
		if permit != nil {
			uncertain = true
			observed.Kind = Indeterminate
			observed.FinishedAt = time.Now()
			s.observe(observed)
			s.scheduler.Release(permit, scheduler.Unknown)
			s.mu.Lock()
			s.uncertain[permit] = struct{}{}
			s.mu.Unlock()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-front:
			decoder := pgproto3.NewBackend(bytes.NewReader(event.data), io.Discard)
			decoder.SetMaxBodyLen(s.cfg.MaxMessageBytes)
			m, err := decoder.Receive()
			if err != nil {
				return
			}
			if _, ok := m.(*pgproto3.Terminate); ok {
				return
			}
			acquire, err := phase.before(m)
			if err != nil {
				sendError(ss.client, "0A000", "unsupported query protocol; connection closed")
				return
			}
			if acquire {
				var classification domain.Classification
				if s.cfg.ClassifySession != nil {
					classification = s.cfg.ClassifySession(user, s.cfg.Database, identity)
				} else {
					classification = s.cfg.Classify(user, s.cfg.Database)
				}
				observed = Observation{ID: s.nextObservation.Add(1), Kind: Arrived, Classification: classification, LimitRevision: classification.ActivationRevision, ArrivedAt: time.Now()}
				s.observe(observed)
				classification.BufferedBytes = int64(len(event.data))
				permit, err = s.scheduler.Acquire(ctx, classification)
				if err != nil {
					observed.Kind = AdmissionRejected
					observed.TimedOut = errors.Is(err, context.DeadlineExceeded) || errors.Is(err, scheduler.ErrExpired)
					observed.FinishedAt = time.Now()
					s.observe(observed)
					sendError(ss.client, "57014", "admission canceled or unavailable; connection closed")
					return
				}
				observed.AcquiredAt = permit.StartedAt()
				observed.LimitRevision = permit.LimitRevision()
				observed.Classification = permit.Classification()
				observed.Kind = Admitted
				s.observe(observed)
			}
			if ctx.Err() != nil {
				return
			}
			ss.upstream.SetWriteDeadline(time.Now().Add(s.cfg.IOTimeout))
			if writeAll(ss.upstream, event.data) != nil {
				return
			}
		case event := <-back:
			if event.err != nil {
				return
			}
			if event.kind == 'G' || event.kind == 'H' || event.kind == 'W' || event.kind == 's' {
				sendError(ss.client, "0A000", "COPY and suspended portals are unsupported; outcome may be uncertain")
				return
			}
			if event.kind == 'E' {
				observed.Failed = true
			}
			if event.kind == 'Z' {
				release, err := phase.ready(event.ready)
				if err != nil {
					return
				}
				if release {
					observed.Kind = Completed
					observed.FinishedAt = time.Now()
					s.observe(observed)
					outcome := scheduler.Finished
					if observed.Failed {
						outcome = scheduler.Failed
					}
					s.scheduler.Release(permit, outcome)
					permit = nil
				}
			}
			ss.client.SetWriteDeadline(time.Now().Add(s.cfg.IOTimeout))
			if writeAll(ss.client, event.data) != nil {
				return
			}
		}
	}
}

func sendError(c net.Conn, code, message string) {
	c.SetWriteDeadline(time.Now().Add(time.Second))
	raw, _ := (&pgproto3.ErrorResponse{Severity: "FATAL", Code: code, Message: message}).Encode(nil)
	_ = writeAll(c, raw)
}
func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, e := w.Write(p)
		if e != nil {
			return e
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}

// Observation contains no SQL, parameters, credentials or rows. One admission
// unit is a complete transaction or autocommit exchange, not each SQL message.
type ObservationKind = observation.Kind

const (
	Arrived           = observation.Arrived
	Admitted          = observation.Admitted
	Completed         = observation.Completed
	AdmissionRejected = observation.AdmissionRejected
	Indeterminate     = observation.Indeterminate
)

type Observation = observation.Event

func (s *Server) observe(o Observation) {
	if s.cfg.Observe != nil {
		s.cfg.Observe(o)
	}
}

// BackendPIDs lists authenticated sessions only. Cancellation secrets never leave the server.
func (s *Server) BackendPIDs() []int32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]int32, 0, len(s.cancels))
	for _, ss := range s.cancels {
		if ss.ready {
			ids = append(ids, int32(ss.backendPID))
		}
	}
	return ids
}

// A client may report channel-binding support (y) when the upstream itself
// offers only non-binding SCRAM. Reject y if PLUS was stripped by this proxy;
// accepting it there would hide a channel-binding downgrade. Never rewrite GS2.
func nonBindingSCRAMInitial(data []byte, upstreamBinding bool) bool {
	return bytes.HasPrefix(data, []byte("n,,")) || (!upstreamBinding && bytes.HasPrefix(data, []byte("y,,")))
}
