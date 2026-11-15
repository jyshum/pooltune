package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"pooltune/internal/graphrunner"
	"pooltune/internal/hostedapp"
	"pooltune/internal/hostedquery"
	"pooltune/internal/labdb"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	interval := flag.Duration("interval", time.Minute, "time between monitoring samples")
	window := flag.Duration("window", 5*time.Minute, "time covered by one objective window")
	queue := flag.Duration("queue-interval", 2*time.Second, "time between diagnosis queue checks")
	flag.Parse()
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	origin := os.Getenv("POOLTUNE_APP_ORIGIN")
	address := net.JoinHostPort("", port)
	if origin == "" {
		host := os.Getenv("RAILWAY_PUBLIC_DOMAIN")
		if host != "" {
			origin = "https://" + host
		} else {
			address = net.JoinHostPort("127.0.0.1", port)
			origin = "http://" + address
		}
	}
	if parsed, err := url.Parse(origin); err == nil && parsed.Scheme == "http" {
		address = net.JoinHostPort(parsed.Hostname(), port)
	}
	key, err := base64.StdEncoding.DecodeString(os.Getenv("POOLTUNE_APP_KEY"))
	if err != nil || len(key) != 32 {
		return errors.New("set POOLTUNE_APP_KEY to 32 random bytes in standard base64")
	}
	stateDSN := os.Getenv("POOLTUNE_WORKER_STATE_DSN")
	state, err := labdb.StateConfig(stateDSN)
	if err != nil {
		return errors.New("set a valid Pooltune state database route")
	}
	state.RuntimeParams["application_name"] = "pooltune-web"
	state.RuntimeParams["statement_timeout"] = "5s"
	poolConfig, err := pgxpool.ParseConfig(stateDSN)
	if err != nil {
		return errors.New("invalid Pooltune state database pool configuration")
	}
	poolConfig.ConnConfig = state
	poolConfig.MaxConns = 24
	setup, done := context.WithTimeout(ctx, 16*time.Second)
	defer done()
	pool, err := pgxpool.NewWithConfig(setup, poolConfig)
	if err != nil {
		return errors.New("could not prepare the Pooltune state database")
	}
	defer pool.Close()
	var investigate hostedquery.EnrolledLookupInvestigator
	if os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("POOLTUNE_ANTHROPIC_MODEL") != "" {
		investigate, err = graphrunner.ProjectLookupInvestigator(stateDSN)
		if err != nil {
			return err
		}
	}
	app, err := hostedapp.New(setup, pool, hostedapp.Config{Origin: origin, InviteCode: os.Getenv("POOLTUNE_SIGNUP_CODE"),
		Key: key, Sampling: hostedquery.SamplingPolicy{Interval: *interval, Window: *window}, QueueInterval: *queue, Investigate: investigate})
	if err != nil {
		return fmt.Errorf("dashboard initialization failed: %s", safeStartupError(err))
	}
	done()
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	workers := make(chan struct{})
	go func() {
		defer close(workers)
		app.RunWorkers(ctx)
	}()
	server := &http.Server{Addr: address, Handler: app, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 18 * time.Second, WriteTimeout: 22 * time.Second, IdleTimeout: time.Minute}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if server.Shutdown(shutdown) != nil {
			_ = server.Close()
		}
	}()
	slog.Info("dashboard listening", "origin", origin, "diagnosis", investigate != nil)
	err = server.ListenAndServe()
	stop()
	<-workers
	<-drained
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func safeStartupError(err error) string {
	switch err.Error() {
	case "POOLTUNE_APP_ORIGIN must be the dashboard origin without a path", "the dashboard requires HTTPS outside loopback",
		"set POOLTUNE_SIGNUP_CODE to a private code of 16 to 256 bytes", "invalid preview monitoring intervals":
		return err.Error()
	default:
		return "check the Pooltune state database and application configuration"
	}
}
