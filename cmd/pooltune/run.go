package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"pooltune/internal/config"
)

func run(ctx context.Context, args []string) error {
	if len(args) == 0 || !validMode(args[0]) {
		return errors.New("usage: pooltune traffic|control|gateway [-listen 127.0.0.1:8080] [-config path]")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	addr := flags.String("listen", "127.0.0.1:8080", "local health address")
	path := flags.String("config", "", "configuration JSON file")
	if err := flags.Parse(args[1:]); err != nil {
		return errors.New("invalid command flags")
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected command arguments")
	}
	if *path != "" {
		f, err := os.Open(*path)
		if err != nil {
			return errors.New("cannot open config")
		}
		defer f.Close()
		c, err := config.LoadConfig(f)
		if err != nil {
			return fmt.Errorf("config rejected: %w", err)
		}
		if err := config.ValidateForMode(c, args[0]); err != nil {
			return fmt.Errorf("config rejected: %w", err)
		}
	}
	ln, err := listenLocal(*addr)
	if err != nil {
		return err
	}
	return serve(ctx, ln, args[0])
}

func validMode(mode string) bool { return mode == "traffic" || mode == "control" || mode == "gateway" }

func listenLocal(addr string) (net.Listener, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, errors.New("invalid listen address")
	}
	ip := net.ParseIP(host)
	n, err := strconv.Atoi(port)
	if ip == nil || !ip.IsLoopback() || err != nil || n < 0 || n > 65535 {
		return nil, errors.New("listen address must be a literal loopback IP and valid port")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, errors.New("cannot listen on local address")
	}
	return ln, nil
}

func healthHandler(mode string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("alive\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, mode+" unavailable", http.StatusServiceUnavailable)
	})
	return mux
}

func serve(ctx context.Context, ln net.Listener, mode string) error {
	server := &http.Server{Handler: healthHandler(mode), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			server.Shutdown(shutdownCtx)
		case <-done:
		}
	}()
	err := server.Serve(ln)
	close(done)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
