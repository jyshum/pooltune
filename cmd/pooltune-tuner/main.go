package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"pooltune/internal/admin"
	"pooltune/internal/durable"
	"pooltune/internal/hostmetrics"
	"pooltune/internal/provider"
	localruntime "pooltune/internal/runtime"
	"pooltune/internal/store"
	"pooltune/internal/telemetry"
	"pooltune/internal/tuning"
	"syscall"
	"time"
)

func main() {
	file := flag.String("config", "", "Absolute path to an approved local-tuner-1 profile")
	managedFile := flag.String("managed-config", "", "Absolute managed provider profile; Azure experimental control, Supabase read-only safety-gated monitoring")
	resume := flag.Bool("managed-resume", false, "Explicitly resume an approved Azure managed profile after verified startup")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var err error
	if *managedFile != "" && *file == "" {
		err = runManaged(ctx, *managedFile, *resume)
	} else if *managedFile == "" && !*resume {
		err = run(ctx, *file)
	} else {
		err = tuning.ErrUnsafe
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Pooltune tuner stopped: configuration, ownership or private service unavailable. Review the profile, provider capability gates and retained state.")
		os.Exit(1)
	}
}
func run(ctx context.Context, path string) error {
	if !filepath.IsAbs(path) {
		return tuning.ErrUnsafe
	}
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	cfg, e := tuning.LoadConfig(f)
	f.Close()
	if e != nil {
		return e
	}
	adminConn, appConn, monitor, e := cfg.Connections()
	if e != nil {
		return e
	}
	files, e := durable.Open(cfg.Directory)
	if e != nil {
		return e
	}
	defer files.Close()
	// Validate connection scope before contacting a database for the shared lease.
	db, e := store.OpenStore(filepath.Join(cfg.Directory, "control.db"))
	if e != nil {
		return e
	}
	defer db.Close()
	pool, e := provider.NewLocalPool(provider.LocalPoolConfig{Target: cfg.Target, Admin: adminConn, Application: appConn, Monitor: monitor, Allocation: cfg.Bounds.Maximum, Journal: db})
	if e != nil {
		return e
	}
	defer pool.Close()
	owner, e := localruntime.AcquireTuningOwner(ctx, monitor)
	if e != nil {
		return e
	}
	defer owner.Close()
	service, e := tuning.New(cfg.Target, cfg.Bounds, pool, db, files)
	if e != nil {
		return e
	}
	claim, e := files.Load("claim.token")
	if os.IsNotExist(e) {
		var b [32]byte
		if _, e = rand.Read(b[:]); e != nil {
			return e
		}
		claim = []byte(base64.RawURLEncoding.EncodeToString(b[:]))
		e = files.Save("claim.token", claim)
	}
	if e != nil {
		return e
	}
	auth, e := admin.NewAuth(db, string(claim), time.Now().Add(24*time.Hour), 8*time.Hour)
	if e != nil {
		return e
	}
	assets, e := os.OpenRoot(cfg.Assets)
	if e != nil {
		return e
	}
	defer assets.Close()
	history, stopHistory := telemetry.WrapEnv(ctx, service, "local")
	defer stopHistory()
	handler, e := admin.NewHandler(admin.Config{Origin: "http://" + cfg.Listen, LocalHTTP: true}, auth, history, assets.FS())
	if e != nil {
		return e
	}
	listener, e := net.Listen("tcp", cfg.Listen)
	if e != nil {
		return e
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	fmt.Printf("Private tuning dashboard: http://%s\nAutomation starts frozen. Claim token is in the private state directory.\n", cfg.Listen)
	sampler := &hostmetrics.Sampler{}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-done:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-ticker.C:
			tickCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			reading, err := sampler.Sample(tickCtx)
			if err != nil {
				reading.At = time.Time{}
			}
			_ = service.Tick(tickCtx, time.Now(), reading)
			cancel()
		}
	}
}
