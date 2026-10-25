package pgwire

import (
	"context"
	"github.com/jackc/pgx/v5/pgproto3"
	"net"
	"pooltune/internal/domain"
	"pooltune/internal/policy"
	"pooltune/internal/scheduler"
	"runtime"
	"testing"
	"time"
)

func testScheduler(t *testing.T) *scheduler.Scheduler {
	t.Helper()
	p, e := policy.Compile(policy.Policy{SchemaVersion: "1", Revision: "p1", GuardrailVersion: "g1", ClassWeights: policy.ClassWeights{Critical: 1, Normal: 1, Background: 1}}, policy.Guardrails{Version: "g1", MaxReserveFraction: .9, MinClassWeight: 1, MaxClassWeight: 10}, policy.Registry{Fallback: policy.Fallback{QueueWait: time.Second, QueueLimit: 10, ConcurrencyCap: 10}})
	if e != nil {
		t.Fatal(e)
	}
	s, e := scheduler.New(scheduler.Config{MaxActive: 10, MaxQueued: 10, MaxQueuedBytes: 1 << 20, MaxTenants: 10, MaxWorkloads: 10, MaxTenantQueued: 10, MaxTenantActive: 10, RatePerSecond: 1000, RateBurst: 100}, scheduler.Snapshot{Limits: domain.LimitSnapshot{Target: "db", Path: domain.PathPostgres, Revision: "c1", Effective: 1, CohortBudgets: map[string]int{"stable": 1}, GuardrailVersion: "g1", Epoch: 1}, Policies: map[string]*policy.CompiledPolicy{"stable": p}})
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func testClassification(user, db string) domain.Classification {
	return domain.Classification{Class: domain.ClassNormal, Cohort: "stable", Workload: "web", FairnessKey: user, Revision: "p1", QueueDeadline: time.Now().Add(5 * time.Second), ConcurrencyCap: 10, QueueLimit: 10}
}

type testClient struct {
	c   net.Conn
	f   *pgproto3.Frontend
	pid uint32
	key []byte
}

func connectTest(t *testing.T, addr string) *testClient {
	t.Helper()
	c, e := net.Dial("tcp", addr)
	if e != nil {
		t.Fatal(e)
	}
	c.SetDeadline(time.Now().Add(3 * time.Second))
	f := pgproto3.NewFrontend(c, c)
	f.Send(&pgproto3.StartupMessage{ProtocolVersion: pgproto3.ProtocolVersion30, Parameters: map[string]string{"user": "original_user", "database": "demo"}})
	if e = f.Flush(); e != nil {
		t.Fatal(e)
	}
	out := &testClient{c: c, f: f}
	t.Cleanup(func() { c.Close() })
	for {
		m, e := f.Receive()
		if e != nil {
			t.Fatal(e)
		}
		switch v := m.(type) {
		case *pgproto3.BackendKeyData:
			out.pid = v.ProcessID
			out.key = append([]byte(nil), v.SecretKey...)
		case *pgproto3.ReadyForQuery:
			return out
		case *pgproto3.ErrorResponse:
			t.Fatal(v.Message)
		}
	}
}
func (c *testClient) query(t *testing.T, q string) byte {
	t.Helper()
	c.f.Send(&pgproto3.Query{String: q})
	if e := c.f.Flush(); e != nil {
		t.Fatal(e)
	}
	for {
		m, e := c.f.Receive()
		if e != nil {
			t.Fatal(e)
		}
		if r, ok := m.(*pgproto3.ReadyForQuery); ok {
			return r.TxStatus
		}
	}
}
func eventually(t *testing.T, f func() bool) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		if f() {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("condition not reached")
}

func startFixture(t *testing.T, configure ...func(*TargetConfig)) (string, *Server, *scheduler.Scheduler, <-chan string) {
	t.Helper()
	up, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { up.Close() })
	seen := make(chan string, 32)
	go func() {
		for {
			c, e := up.Accept()
			if e != nil {
				return
			}
			go func() {
				defer c.Close()
				c.SetDeadline(time.Now().Add(5 * time.Second))
				b := pgproto3.NewBackend(c, c)
				m, e := b.ReceiveStartupMessage()
				if e != nil {
					return
				}
				st, ok := m.(*pgproto3.StartupMessage)
				if !ok {
					return
				}
				seen <- "user:" + st.Parameters["user"]
				b.Send(&pgproto3.AuthenticationOk{})
				b.Send(&pgproto3.BackendKeyData{ProcessID: 42, SecretKey: []byte{0, 0, 0, 7}})
				b.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
				if b.Flush() != nil {
					return
				}
				for {
					m, e := b.Receive()
					if e != nil {
						return
					}
					switch v := m.(type) {
					case *pgproto3.Query:
						seen <- v.String
						if v.String == "SLOW" {
							time.Sleep(100 * time.Millisecond)
						}
						if v.String == "COMMIT" {
							return
						} // reply lost after forwarding
						status := byte('I')
						if v.String == "BEGIN" {
							status = 'T'
						}
						if v.String == "ERROR" {
							status = 'E'
							b.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "22012", Message: "test error"})
						}
						b.Send(&pgproto3.CommandComplete{CommandTag: []byte("OK")})
						b.Send(&pgproto3.ReadyForQuery{TxStatus: status})
						if b.Flush() != nil {
							return
						}
					case *pgproto3.Terminate:
						return
					}
				}
			}()
		}
	}()
	s := testScheduler(t)
	cfg := TargetConfig{Target: "db", Database: "demo", Upstream: up.Addr().String(), LocalTest: true, MaxSessions: 4, MaxHandshakes: 4, MaxMessageBytes: 1 << 20, HandshakeTimeout: time.Second, IOTimeout: 3 * time.Second, Classify: testClassification}
	for _, f := range configure {
		f(&cfg)
	}
	srv, e := NewServer(cfg, s)
	if e != nil {
		t.Fatal(e)
	}
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, l) }()
	t.Cleanup(func() { cancel(); <-done })
	return l.Addr().String(), srv, s, seen
}

func TestServerPreservesRoleBatchAndUncertainOccupancy(t *testing.T) {
	addr, srv, s, seen := startFixture(t)
	c := connectTest(t, addr)
	if got := <-seen; got != "user:original_user" {
		t.Fatalf("role changed %s", got)
	}
	if c.pid == 42 && len(c.key) == 4 && c.key[3] == 7 {
		t.Fatal("upstream cancellation key leaked")
	}
	if c.query(t, "BEGIN") != 'T' || s.Stats().Active != 1 {
		t.Fatal("begin not held")
	}
	if c.query(t, "ERROR") != 'E' || s.Stats().Active != 1 {
		t.Fatal("error transaction released")
	}
	if c.query(t, "ROLLBACK") != 'I' {
		t.Fatal("rollback failed")
	}
	eventually(t, func() bool { return s.Stats().Active == 0 })
	c.query(t, "select 1; select 2")
	for _, want := range []string{"BEGIN", "ERROR", "ROLLBACK", "select 1; select 2"} {
		if got := <-seen; got != want {
			t.Fatalf("batch altered: %q", got)
		}
	}
	c.query(t, "BEGIN")
	c.c.Close()
	eventually(t, func() bool { return s.Stats().Uncertain == 1 && srv.UncertainCount() == 1 })
}

func TestQueuedCancelNeverForwardsWork(t *testing.T) {
	addr, _, s, seen := startFixture(t)
	a := connectTest(t, addr)
	<-seen
	a.query(t, "BEGIN")
	<-seen
	b := connectTest(t, addr)
	<-seen
	b.f.Send(&pgproto3.Query{String: "must not run"})
	if e := b.f.Flush(); e != nil {
		t.Fatal(e)
	}
	eventually(t, func() bool { return s.Stats().Queued == 1 })
	c, e := net.Dial("tcp", addr)
	if e != nil {
		t.Fatal(e)
	}
	packet, _ := (&pgproto3.CancelRequest{ProcessID: b.pid, SecretKey: b.key}).Encode(nil)
	c.Write(packet)
	c.Close()
	eventually(t, func() bool { return s.Stats().Queued == 0 })
	select {
	case q := <-seen:
		t.Fatalf("canceled work forwarded %s", q)
	default:
	}
	if s.Stats().Active != 1 {
		t.Fatal("cancel affected other session")
	}
}

func TestConfigurationRequiresVerifiedTLS(t *testing.T) {
	s := testScheduler(t)
	if _, e := NewServer(TargetConfig{Target: "db", Upstream: "example.com:5432"}, s); e == nil {
		t.Fatal("accepted unverified connection profile")
	}
}

func TestListenerFailureClosesPendingHandshakes(t *testing.T) {
	s := testScheduler(t)
	srv, err := NewServer(TargetConfig{Target: "db", Database: "demo", Upstream: "127.0.0.1:1", LocalTest: true, MaxSessions: 1, MaxHandshakes: 1, MaxMessageBytes: 1024, HandshakeTimeout: 3 * time.Second, IOTimeout: 3 * time.Second, Classify: testClassification}, s)
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, l) }()
	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	eventually(t, func() bool { srv.mu.Lock(); defer srv.mu.Unlock(); return len(srv.connections) == 1 })
	l.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("missing listener error")
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("listener failure stranded handshake")
	}
}

func TestDisconnectDuringCommitRetainsBothReservations(t *testing.T) {
	addr, srv, s, seen := startFixture(t)
	c := connectTest(t, addr)
	<-seen
	c.query(t, "BEGIN")
	<-seen
	c.f.Send(&pgproto3.Query{String: "COMMIT"})
	if err := c.f.Flush(); err != nil {
		t.Fatal(err)
	}
	if q := <-seen; q != "COMMIT" {
		t.Fatal(q)
	}
	eventually(t, func() bool { return s.Stats().Uncertain == 1 })
	eventually(t, func() bool { srv.mu.Lock(); defer srv.mu.Unlock(); return len(srv.connections) == 0 })
	if len(srv.sessions) != 1 {
		t.Fatal("uncertain backend lost physical reservation")
	}
	select {
	case q := <-seen:
		t.Fatalf("write replayed: %s", q)
	default:
	}
}
func TestRetiredCancelCannotReachNewSession(t *testing.T) {
	addr, srv, s, seen := startFixture(t)
	a := connectTest(t, addr)
	<-seen
	a.c.Close()
	eventually(t, func() bool { srv.mu.Lock(); defer srv.mu.Unlock(); return len(srv.cancels) == 0 })
	b := connectTest(t, addr)
	<-seen
	packet, _ := (&pgproto3.CancelRequest{ProcessID: a.pid, SecretKey: a.key}).Encode(nil)
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	c.Write(packet)
	c.Close()
	b.query(t, "BEGIN")
	<-seen
	if s.Stats().Active != 1 {
		t.Fatal("retired cancel affected new lease")
	}
	b.query(t, "ROLLBACK")
	<-seen
}
func TestIngressRejectsDifferentSchedulerTarget(t *testing.T) {
	_, err := NewServer(TargetConfig{Target: "other-db", Database: "demo", Upstream: "127.0.0.1:1", LocalTest: true, MaxSessions: 1, MaxHandshakes: 1, MaxMessageBytes: 1024, HandshakeTimeout: time.Second, IOTimeout: time.Second, Classify: testClassification}, testScheduler(t))
	if err == nil {
		t.Fatal("accepted scheduler belonging to another target")
	}
}

func TestSCRAMGS2DoesNotHideBindingDowngrade(t *testing.T) {
	for _, tc := range []struct {
		header     string
		plus, want bool
	}{{"n,,", false, true}, {"n,,", true, true}, {"y,,", false, true}, {"y,,", true, false}, {"p=tls-server-end-point,,", false, false}, {"n,a=other,", false, false}, {"y,", false, false}} {
		if got := nonBindingSCRAMInitial([]byte(tc.header+"n=user,r=nonce"), tc.plus); got != tc.want {
			t.Fatalf("header %q plus=%v accepted=%v", tc.header, tc.plus, got)
		}
	}
}

func TestPreserveReadIdleKeepsLongQueryAndOpenTransaction(t *testing.T) {
	addr, _, sched, _ := startFixture(t, func(c *TargetConfig) { c.IOTimeout = 30 * time.Millisecond; c.PreserveReadIdle = true })
	c := connectTest(t, addr)
	if c.query(t, "SLOW") != 'I' {
		t.Fatal("long query interrupted")
	}
	c.query(t, "BEGIN")
	time.Sleep(100 * time.Millisecond)
	if sched.Stats().Active != 1 || sched.Stats().Uncertain != 0 {
		t.Fatal("idle transaction interrupted")
	}
	if c.query(t, "ROLLBACK") != 'I' {
		t.Fatal("idle transaction lost")
	}
}
