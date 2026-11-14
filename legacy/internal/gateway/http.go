package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

func strictJSON(raw []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil {
		return ErrInvalid
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return ErrInvalid
	}
	return nil
}
func NewHandler(service *Service, localTest bool) (http.Handler, error) {
	if service == nil || service.Store == nil || service.Model == nil || !service.Profile.valid() {
		return nil, ErrInvalid
	}
	slots := make(chan struct{}, 16)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			host, _, e := net.SplitHostPort(r.RemoteAddr)
			ip := net.ParseIP(host)
			if !localTest || e != nil || ip == nil || !ip.IsLoopback() {
				http.Error(w, "HTTPS required", 400)
				return
			}
		}
		if r.Method != "POST" || r.Header.Get("Origin") != "" || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "unsupported request", 400)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "gateway busy", 503)
			return
		}
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(5 * time.Second))
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(service.Profile.Timeout + 5*time.Second))
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") || len(r.Header.Values("Authorization")) != 1 {
			http.Error(w, "access denied", 401)
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")
		if _, e := service.Store.Authorize(token); e != nil {
			http.Error(w, "access denied", 401)
			return
		}
		raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(service.Profile.MaxPayloadBytes+2048)))
		if e != nil {
			http.Error(w, "invalid payload", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		switch r.URL.Path {
		case "/v1/runs":
			var empty struct{}
			if strictJSON(raw, &empty) != nil {
				http.Error(w, "invalid payload", 400)
				return
			}
			run, e := service.Start(r.Context(), token)
			if e != nil {
				http.Error(w, "run denied", 429)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"run": run})
		case "/v1/step":
			var req StepRequest
			if strictJSON(raw, &req) != nil {
				http.Error(w, "invalid payload", 400)
				return
			}
			out, e := service.Step(r.Context(), token, req)
			if e != nil {
				status := 503
				if e == ErrInvalid {
					status = 400
				} else if e == ErrDuplicate {
					status = 409
				} else if e == ErrBudget || e == ErrDenied {
					status = 429
				}
				http.Error(w, "step denied or outcome uncertain", status)
				return
			}
			json.NewEncoder(w).Encode(out)
		default:
			http.NotFound(w, r)
		}
	}), nil
}
