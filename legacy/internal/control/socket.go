package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"pooltune/internal/domain"
	"regexp"
	"time"
)

var revisionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// Serve exposes only local control on the engine's exclusively owned directory.
func (e *Engine) Serve(ctx context.Context) error {
	e.mu.Lock()
	if e.closed || e.serving {
		e.mu.Unlock()
		return ErrInvalid
	}
	e.serving = true
	e.mu.Unlock()
	defer func() { e.mu.Lock(); e.serving = false; e.mu.Unlock() }()

	socket := filepath.Join(e.cfg.Directory, "control.sock")
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return ErrInvalid
		}
		if err = os.Remove(socket); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err = os.Chmod(socket, 0600); err != nil {
		return err
	}
	mux := http.NewServeMux()
	e.measurementRoutes(mux)
	e.capacityRoutes(mux)
	mux.HandleFunc("POST /activate", func(w http.ResponseWriter, r *http.Request) {
		var env domain.ActivationEnvelope
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
		if err != nil || decode(data, &env) != nil {
			http.Error(w, "invalid activation", 400)
			return
		}
		ack, err := e.Activate(r.Context(), env)
		if err != nil {
			http.Error(w, "activation rejected or uncertain", 409)
			return
		}
		json.NewEncoder(w).Encode(ack)
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		revision := r.URL.Query().Get("revision")
		if !revisionPattern.MatchString(revision) {
			http.Error(w, "invalid revision", 400)
			return
		}
		json.NewEncoder(w).Encode(e.Status(domain.RevisionID(revision)))
	})
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(e.View()) })
	mux.HandleFunc("POST /ack-fallback", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Revision domain.RevisionID
			Checksum string
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
		if err != nil || decode(data, &input) != nil {
			http.Error(w, "invalid acknowledgement", 400)
			return
		}
		if err = e.AcknowledgeFallback(input.Revision, input.Checksum); err != nil {
			http.Error(w, "acknowledgement rejected", 409)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /recover", func(w http.ResponseWriter, r *http.Request) {
		var confirmed domain.ActivationEnvelope
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
		if err != nil || decode(data, &confirmed) != nil {
			http.Error(w, "invalid recovery", 400)
			return
		}
		if err := e.RecoverConfirmed(r.Context(), confirmed); err != nil {
			http.Error(w, "recovery blocked", 409)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /rollback", func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Revision domain.RevisionID }
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
		if err != nil || decode(raw, &input) != nil {
			http.Error(w, "invalid rollback", 400)
			return
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		if input.Revision != e.state.Active.Revision {
			http.Error(w, "stale rollback", 409)
			return
		}
		if err = e.fallbackLocked(); err != nil {
			http.Error(w, "rollback uncertain", 503)
			return
		}
		w.WriteHeader(204)
	})
	bounded := make(chan struct{}, 4)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			http.Error(w, "browser access denied", 403)
			return
		}
		select {
		case bounded <- struct{}{}:
			defer func() { <-bounded }()
		default:
			http.Error(w, "control busy", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		mux.ServeHTTP(w, r)
	}), ReadHeaderTimeout: time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 8192}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			server.Close()
		case <-done:
		}
	}()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
