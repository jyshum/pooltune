package burst

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"pooltune/internal/admin"
	"pooltune/internal/store"
	"pooltune/internal/telemetry"
	"strings"
	"time"
)

// Dashboard is a private, authenticated control server. Close it before Runtime.
type Dashboard struct {
	URL    string
	Errors <-chan error
	close  func()
}

func (d *Dashboard) Close() { d.close() }
func overlapsPath(a, b string) bool {
	rel, e := filepath.Rel(a, b)
	return e == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}
func (r *Runtime) StartDashboard(listen, assets, secretRoot string) (*Dashboard, error) {
	host, _, e := net.SplitHostPort(listen)
	ip := net.ParseIP(host)
	if e != nil || ip == nil || !ip.IsLoopback() || r.files == nil {
		return nil, ErrUnsafe
	}
	root, e := filepath.EvalSymlinks(assets)
	if e != nil || !filepath.IsAbs(root) {
		return nil, ErrUnsafe
	}
	for _, dir := range []string{r.cfg.Directory, secretRoot} {
		real, e := filepath.EvalSymlinks(dir)
		if e != nil || overlapsPath(root, real) || overlapsPath(real, root) {
			return nil, ErrUnsafe
		}
	}
	fs, e := os.OpenRoot(root)
	if e != nil {
		return nil, e
	}
	db, e := store.OpenStore(filepath.Join(r.cfg.Directory, "dashboard.db"))
	if e != nil {
		fs.Close()
		return nil, e
	}
	closeFiles := func() { db.Close(); fs.Close() }
	claim, e := r.files.Load("claim.token")
	if os.IsNotExist(e) {
		buf := make([]byte, 32)
		if _, e = rand.Read(buf); e == nil {
			claim = []byte(base64.RawURLEncoding.EncodeToString(buf))
			e = r.files.Save("claim.token", claim)
		}
	}
	if e != nil {
		closeFiles()
		return nil, e
	}
	auth, e := admin.NewAuth(db, string(claim), time.Now().Add(24*time.Hour), 8*time.Hour)
	if e != nil {
		closeFiles()
		return nil, e
	}
	listener, e := net.Listen("tcp", listen)
	if e != nil {
		closeFiles()
		return nil, e
	}
	origin := "http://" + listener.Addr().String()
	history, stopHistory := telemetry.WrapEnv(context.Background(), DashboardBackend{r.Controller}, "managed")
	handler, e := admin.NewHandler(admin.Config{Origin: origin, LocalHTTP: true}, auth, history, fs.FS())
	if e != nil {
		stopHistory()
		listener.Close()
		closeFiles()
		return nil, e
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	return &Dashboard{URL: origin, Errors: done, close: func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		server.Shutdown(ctx)
		stopHistory()
		closeFiles()
	}}, nil
}
