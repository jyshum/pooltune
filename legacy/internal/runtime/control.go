package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"pooltune/internal/admin"
	"pooltune/internal/agent"
	"pooltune/internal/control"
	"pooltune/internal/domain"
	"pooltune/internal/durable"
	"pooltune/internal/secrets"
	"pooltune/internal/store"
	"strings"
	"time"
)

type AdminConfig struct {
	Listen, Origin, Assets, SecretRoot, ClaimFile string
	ClaimExpires                                  time.Time
	SessionLifetime                               time.Duration
}
type ControlConfig struct {
	RecoverLocalREST                           bool
	CapacityCalibrationFile                    string
	RecoverLocalPostgres                       bool
	Admin                                      *AdminConfig
	Schema, Directory, Socket, CalibrationFile string
	Service                                    control.ServiceConfig
	ManagedAI                                  *ManagedAIConfig
}
type ManagedAIConfig struct {
	Endpoint, SecretRoot, InstallationTokenFile string
	Timeout                                     time.Duration
}

func LoadControl(r io.Reader) (ControlConfig, error) {
	var c ControlConfig
	raw, err := io.ReadAll(io.LimitReader(r, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return c, ErrConfig
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(&struct{}{}) != io.EOF || c.Schema != "local-control-1" || !filepath.IsAbs(c.Directory) || !filepath.IsAbs(c.Socket) || c.Service.ResourceSource != "local-host" {
		return ControlConfig{}, ErrConfig
	}
	if (c.RecoverLocalPostgres && c.Service.Path != domain.PathPostgres) || (c.RecoverLocalREST && c.Service.Path != domain.PathREST) {
		return ControlConfig{}, ErrConfig
	}
	if a := c.Admin; a != nil {
		if !localAddress(a.Listen) || a.Origin != "http://"+a.Listen || !filepath.IsAbs(a.Assets) || !filepath.IsAbs(a.SecretRoot) || !filepath.IsAbs(a.ClaimFile) || a.SessionLifetime <= 0 || a.SessionLifetime > 24*time.Hour || a.ClaimExpires.IsZero() {
			return ControlConfig{}, ErrConfig
		}
	}
	if p := c.Service.Capacity; p != nil {
		if (c.Service.Path != domain.PathPostgres && c.Service.Path != domain.PathREST) || (c.Service.Path == domain.PathREST && p.Guardrails.HTTPEnvelope <= 0) || !filepath.IsAbs(c.CapacityCalibrationFile) || len(p.EvidenceSHA256) != 64 {
			return ControlConfig{}, ErrConfig
		}
		f, e := secrets.Open(filepath.Dir(c.CapacityCalibrationFile))
		if e != nil {
			return ControlConfig{}, ErrConfig
		}
		data, e := f.Read(c.CapacityCalibrationFile)
		f.Close()
		if e != nil {
			return ControlConfig{}, ErrConfig
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != p.EvidenceSHA256 {
			return ControlConfig{}, ErrConfig
		}
	}
	if c.Service.Evaluation != nil {
		if !filepath.IsAbs(c.CalibrationFile) {
			return ControlConfig{}, ErrConfig
		}
		// The hash identifies the owner's reviewed evidence. It does not certify that
		// the evidence is adequate or grant a provider/performance release claim.
		files, err := secrets.Open(filepath.Dir(c.CalibrationFile))
		if err != nil {
			return ControlConfig{}, ErrConfig
		}
		evidence, err := files.Read(c.CalibrationFile)
		files.Close()
		if err != nil {
			return ControlConfig{}, ErrConfig
		}
		digest := sha256.Sum256(evidence)
		if hex.EncodeToString(digest[:]) != c.Service.Evaluation.EvidenceSHA256 {
			return ControlConfig{}, ErrConfig
		}
	} else if c.ManagedAI != nil || c.Service.Agent != nil || c.Service.Trigger != nil {
		return ControlConfig{}, ErrConfig
	}
	if c.ManagedAI != nil && (c.Service.Agent == nil || c.Service.Trigger == nil) {
		return ControlConfig{}, ErrConfig
	}
	return c, nil
}

type Control struct {
	admin       http.Handler
	adminListen string
	Service     *control.Service
	client      *control.Client
	coordinator *control.Coordinator
	ledger      *store.Store
	owner       *durable.Files
}

func NewControl(ctx context.Context, c ControlConfig) (*Control, error) {
	// Revalidate programmatic callers with the same file/evidence boundary.
	raw, _ := json.Marshal(c)
	validated, err := LoadControl(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c = validated
	owner, err := durable.Open(c.Directory)
	if err != nil {
		return nil, err
	}
	out := &Control{owner: owner}
	ok := false
	defer func() {
		if !ok {
			out.Close()
		}
	}()
	out.client, err = control.NewClient(c.Socket)
	if err != nil {
		return nil, err
	}
	out.ledger, err = store.OpenStore(filepath.Join(c.Directory, "control.db"))
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"intents", "service"} {
		if err = os.MkdirAll(filepath.Join(c.Directory, name), 0700); err != nil {
			return nil, err
		}
	}
	out.coordinator, err = control.NewCoordinator(c.Service.Target, c.Service.Path, filepath.Join(c.Directory, "intents"), out.ledger, out.client)
	if err != nil {
		return nil, err
	}
	observed, err := out.client.View(ctx)
	if err != nil {
		return nil, err
	}
	if observed.AuthoritySHA256 != control.AuthorityChecksum(c.Service.Guardrails, c.Service.Registry) {
		return nil, ErrConfig
	}
	if !observed.Ready && (c.RecoverLocalPostgres || c.RecoverLocalREST) {
		if err = out.coordinator.Recover(ctx); err != nil {
			return nil, err
		}
	}
	active, err := out.ledger.ActiveRevision(out.coordinator.LedgerTarget())
	if errors.Is(err, store.ErrNoActiveRevision) {
		err = out.coordinator.AdoptInitial(ctx)
	} else if err == nil {
		v, e := out.client.View(ctx)
		if e != nil {
			return nil, e
		}
		if active.ID != string(v.Active.Revision) {
			if strings.HasPrefix(string(v.Active.Revision), "fallback-") || strings.HasPrefix(string(v.Active.Revision), "recovery-") {
				_, err = out.coordinator.SyncFallback(ctx)
			} else {
				_, err = out.coordinator.Reconcile(ctx, v.Active.Revision)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	var model agent.Gateway
	if a := c.ManagedAI; a != nil {
		// Only the installation token is read by customer control, never a provider key.
		if !filepath.IsAbs(a.SecretRoot) || !filepath.IsAbs(a.InstallationTokenFile) {
			return nil, ErrConfig
		}
		token := func() (string, error) {
			f, e := secrets.Open(a.SecretRoot)
			if e != nil {
				return "", ErrConfig
			}
			defer f.Close()
			b, e := f.Read(a.InstallationTokenFile)
			if e != nil {
				return "", ErrConfig
			}
			return strings.TrimSpace(string(b)), nil
		}
		model, err = agent.NewClient(a.Endpoint, token, a.Timeout, false)
		if err != nil {
			return nil, ErrConfig
		}
	}
	c.Service.Directory = filepath.Join(c.Directory, "service")
	out.Service, err = control.NewService(c.Service, out.ledger, out.coordinator, out.client, model)
	if err != nil {
		return nil, err
	}
	if a := c.Admin; a != nil {
		files, e := secrets.Open(a.SecretRoot)
		if e != nil {
			return nil, ErrConfig
		}
		claim, e := files.Read(a.ClaimFile)
		files.Close()
		if e != nil {
			return nil, ErrConfig
		}
		auth, e := admin.NewAuth(out.ledger, strings.TrimSpace(string(claim)), a.ClaimExpires, a.SessionLifetime)
		if e != nil {
			return nil, e
		}
		out.admin, e = admin.NewHandler(admin.Config{Origin: a.Origin, LocalHTTP: true}, auth, out.Service, os.DirFS(a.Assets))
		if e != nil {
			return nil, e
		}
		out.adminListen = a.Listen
	}
	ok = true
	return out, nil
}
func (c *Control) Close() error {
	if c.Service != nil {
		c.Service.Close()
	}
	if c.coordinator != nil {
		c.coordinator.Close()
	}
	if c.ledger != nil {
		c.ledger.Close()
	}
	if c.client != nil {
		c.client.Close()
	}
	if c.owner != nil {
		return c.owner.Close()
	}
	return nil
}
func (c *Control) Serve(ctx context.Context, health net.Listener) error {
	if health == nil || !localAddress(health.Addr().String()) {
		return ErrConfig
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "alive\n") })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !c.Service.Ready() {
			http.Error(w, "control unavailable", 503)
			return
		}
		io.WriteString(w, "ready\n")
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	done := make(chan error, 3)
	count := 2
	var adminServer *http.Server
	if c.admin != nil {
		listener, err := ListenLocal(c.adminListen)
		if err != nil {
			return err
		}
		defer listener.Close()
		adminServer = &http.Server{Handler: c.admin, ReadHeaderTimeout: time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10}
		count++
		go func() { done <- adminServer.Serve(listener) }()
	}
	go func() { done <- c.Service.Run(ctx) }()
	go func() { done <- server.Serve(health) }()
	var first error
	received := 0
	select {
	case <-ctx.Done():
	case first = <-done:
		received++
	}
	cancel()
	server.Close()
	if adminServer != nil {
		adminServer.Close()
	}
	for ; received < count; received++ {
		err := <-done
		if first == nil && err != nil && !errors.Is(err, http.ErrServerClosed) {
			first = err
		}
	}
	if errors.Is(first, http.ErrServerClosed) {
		return nil
	}
	return first
}
func RunControlFile(ctx context.Context, path string, health net.Listener) error {
	f, err := os.Open(path)
	if err != nil {
		return ErrConfig
	}
	defer f.Close()
	cfg, err := LoadControl(f)
	if err != nil {
		return err
	}
	c, err := NewControl(ctx, cfg)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Serve(ctx, health)
}
