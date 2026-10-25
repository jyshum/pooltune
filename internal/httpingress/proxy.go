// Package httpingress owns bounded REST admission to one fixed project origin.
package httpingress

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"pooltune/internal/domain"
	"pooltune/internal/observation"
	"pooltune/internal/scheduler"
	"strings"
	"sync/atomic"
	"time"
)

type TargetConfig struct {
	Target, Upstream string
	// LocalPostgRESTSocket selects a private local PostgREST, stripping /rest/v1.
	// It is mutually exclusive with Upstream and does not disable frontend TLS.
	LocalPostgRESTSocket        string
	ClassifyIdentity            func(domain.RequestContext, Identity) domain.Classification
	Observe                     func(observation.Event)
	LocalTest                   bool
	MaxRequests, MaxHeaderBytes int
	MaxBodyBytes                int64
	Timeout                     time.Duration
	AllowedOrigins              []string
	Verify                      func(string, time.Time) Identity
	Classify                    func(domain.RequestContext) domain.Classification
}
type Handler struct {
	nextObservation atomic.Uint64
	cfg             TargetConfig
	upstream        *url.URL
	scheduler       *scheduler.Scheduler
	slots           chan struct{}
	origins         map[string]bool
	transport       *http.Transport
}

func NewRESTHandler(cfg TargetConfig, s *scheduler.Scheduler) (*Handler, error) {
	upstream := cfg.Upstream
	if cfg.LocalPostgRESTSocket != "" {
		if upstream != "" || !privateSocket(cfg.LocalPostgRESTSocket) {
			return nil, errors.New("private PostgREST socket required")
		}
		upstream = "http://postgrest.local"
	}
	u, err := url.Parse(upstream)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || s == nil || (cfg.Classify == nil && cfg.ClassifyIdentity == nil) || cfg.MaxRequests <= 0 || cfg.MaxRequests > 4096 || cfg.MaxBodyBytes <= 0 || cfg.MaxBodyBytes > 16<<20 || cfg.MaxHeaderBytes < 1024 || cfg.MaxHeaderBytes > 64<<10 || cfg.Timeout <= 0 || cfg.Timeout > time.Hour {
		return nil, errors.New("invalid REST configuration")
	}
	target, p := s.Identity()
	if target != cfg.Target || p != domain.PathREST {
		return nil, errors.New("scheduler target/path mismatch")
	}
	if u.Scheme != "https" && cfg.LocalPostgRESTSocket == "" {
		ip := net.ParseIP(u.Hostname())
		if !cfg.LocalTest || u.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return nil, errors.New("HTTPS required")
		}
	}
	origins := make(map[string]bool)
	if len(cfg.AllowedOrigins) > 32 {
		return nil, errors.New("origin limit")
	}
	for _, origin := range cfg.AllowedOrigins {
		v, e := url.Parse(origin)
		if e != nil || v.Host == "" || v.User != nil || v.Path != "" || v.RawQuery != "" || v.Fragment != "" || origin != v.Scheme+"://"+v.Host {
			return nil, errors.New("invalid CORS origin")
		}
		if v.Scheme != "https" {
			ip := net.ParseIP(v.Hostname())
			if !cfg.LocalTest || v.Scheme != "http" || ip == nil || !ip.IsLoopback() {
				return nil, errors.New("HTTPS origin required")
			}
		}
		origins[origin] = true
	}
	// A fresh HTTP/1 connection prevents Transport's implicit retries on reused
	// connections, including potentially side-effecting GET RPCs. Benchmark before
	// adding a verified no-replay keepalive implementation.
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: cfg.Timeout}).DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, DisableKeepAlives: true, MaxConnsPerHost: cfg.MaxRequests, MaxResponseHeaderBytes: int64(cfg.MaxHeaderBytes), ResponseHeaderTimeout: cfg.Timeout, TLSHandshakeTimeout: cfg.Timeout, ForceAttemptHTTP2: false}
	if cfg.LocalPostgRESTSocket != "" {
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			if !privateSocket(cfg.LocalPostgRESTSocket) {
				return nil, errors.New("private upstream unavailable")
			}
			return (&net.Dialer{Timeout: cfg.Timeout}).DialContext(ctx, "unix", cfg.LocalPostgRESTSocket)
		}
	}
	return &Handler{cfg: cfg, upstream: u, scheduler: s, slots: make(chan struct{}, cfg.MaxRequests), origins: origins, transport: transport}, nil
}
func privateSocket(name string) bool {
	if !filepath.IsAbs(name) {
		return false
	}
	parent, e := os.Lstat(filepath.Dir(name))
	if e != nil || !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return false
	}
	info, e := os.Lstat(name)
	return e == nil && info.Mode()&os.ModeSocket != 0 && info.Mode().Perm()&0077 == 0
}
func (h *Handler) observe(e observation.Event) {
	if h.cfg.Observe != nil {
		h.cfg.Observe(e)
	}
}
func (h *Handler) Close() { h.transport.CloseIdleConnections() }

var methods = map[string]bool{"GET": true, "HEAD": true, "POST": true, "PATCH": true, "PUT": true, "DELETE": true, "OPTIONS": true}
var allowedHeaders = map[string]bool{"authorization": true, "apikey": true, "content-type": true, "prefer": true, "accept": true, "accept-profile": true, "content-profile": true, "range": true, "range-unit": true, "x-client-info": true, "x-supabase-api-version": true}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.LocalTest && r.TLS == nil {
		localError(w, 400, "tls_required")
		return
	}
	escaped := strings.ToLower(r.URL.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") || strings.Contains(r.URL.Path, "\\") || path.Clean(r.URL.Path) != strings.TrimSuffix(r.URL.Path, "/") {
		localError(w, 400, "invalid_path")
		return
	}
	if r.URL.Path != "/rest/v1" && !strings.HasPrefix(r.URL.Path, "/rest/v1/") {
		localError(w, 404, "unsupported_route")
		return
	}
	if !methods[r.Method] {
		localError(w, 405, "unsupported_method")
		return
	}
	headerBytes := len(r.RequestURI)
	for k, values := range r.Header {
		for _, v := range values {
			headerBytes += len(k) + len(v) + 4
		}
	}
	if headerBytes > h.cfg.MaxHeaderBytes {
		localError(w, 431, "headers_too_large")
		return
	}
	if len(r.Header.Values("Authorization")) > 1 || len(r.Header.Values("Apikey")) > 1 || r.Header.Get("Upgrade") != "" {
		localError(w, 400, "unsupported_headers")
		return
	}
	origin := r.Header.Get("Origin")
	if origin != "" && !h.origins[origin] {
		localError(w, 403, "origin_denied")
		return
	}
	w.Header().Add("Vary", "Origin")
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Expose-Headers", "Content-Range, Range-Unit, Preference-Applied, Retry-After, X-Pooltune-Error")
	}
	if r.Method == "OPTIONS" {
		method := r.Header.Get("Access-Control-Request-Method")
		if origin == "" || !methods[method] || method == "OPTIONS" {
			localError(w, 403, "preflight_denied")
			return
		}
		for _, v := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
			if v = strings.ToLower(strings.TrimSpace(v)); v != "" && !allowedHeaders[v] {
				localError(w, 403, "preflight_denied")
				return
			}
		}
		w.Header().Add("Vary", "Access-Control-Request-Method")
		w.Header().Add("Vary", "Access-Control-Request-Headers")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PATCH, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", r.Header.Get("Access-Control-Request-Headers"))
		w.WriteHeader(204)
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		// No verified cohort exists yet. Mark coverage incomplete rather than silently
		// dropping overload rejections from an otherwise successful canary window.
		h.observe(observation.Event{Kind: observation.Unattributed, ArrivedAt: time.Now()})
		h.scheduler.OmitCapacity()
		localError(w, 503, "request_limit")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.Timeout)
	defer cancel()
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(h.cfg.Timeout))
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(h.cfg.Timeout))
	if r.ContentLength > h.cfg.MaxBodyBytes {
		localError(w, 413, "body_too_large")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, h.cfg.MaxBodyBytes+1))
	if err != nil {
		localError(w, 400, "body_read_failed")
		return
	}
	if int64(len(body)) > h.cfg.MaxBodyBytes {
		localError(w, 413, "body_too_large")
		return
	}
	id := Identity{}
	if h.cfg.Verify != nil {
		id = h.cfg.Verify(r.Header.Get("Authorization"), time.Now())
	}
	requestContext := domain.RequestContext{Target: h.cfg.Target, Path: domain.PathREST, Method: r.Method, Route: r.URL.Path}
	if id.Verified {
		requestContext.Principal = id.Subject
		requestContext.Tenant = id.Subject
		requestContext.TrustedTenant = true
	}
	if !id.Verified {
		id = Identity{}
	}
	var classification domain.Classification
	if h.cfg.ClassifyIdentity != nil {
		classification = h.cfg.ClassifyIdentity(requestContext, id)
	} else {
		classification = h.cfg.Classify(requestContext)
	}
	classification.BufferedBytes = int64(headerBytes + len(body))
	event := observation.Event{ID: h.nextObservation.Add(1), Kind: observation.Arrived, Classification: classification, LimitRevision: classification.ActivationRevision, ArrivedAt: time.Now()}
	h.observe(event)
	permit, err := h.scheduler.Acquire(ctx, classification)
	if err != nil {
		event.Kind = observation.AdmissionRejected
		event.FinishedAt = time.Now()
		event.TimedOut = errors.Is(err, context.DeadlineExceeded) || errors.Is(err, scheduler.ErrExpired)
		h.observe(event)
		status := 503
		if errors.Is(err, scheduler.ErrRateLimited) {
			status = 429
		}
		localError(w, status, "admission_unavailable")
		return
	}
	event.Kind = observation.Admitted
	event.AcquiredAt = time.Now()
	event.LimitRevision = permit.LimitRevision()
	h.observe(event)
	failed := true
	overloaded := false
	forwarded := false
	complete := atomic.Bool{}
	defer func() {
		outcome := scheduler.Unknown
		if !forwarded || complete.Load() {
			outcome = scheduler.Finished
			if failed {
				outcome = scheduler.Failed
			}
			if overloaded {
				outcome = scheduler.Overloaded
			}
		}
		h.scheduler.Release(permit, outcome)
		event.FinishedAt = time.Now()
		event.Failed = failed
		event.Kind = observation.Completed
		if outcome == scheduler.Unknown {
			event.Kind = observation.Indeterminate
		}
		h.observe(event)
	}()
	if id.Verified && !time.Now().Before(id.ExpiresAt) {
		localError(w, 401, "credential_expired_while_queued")
		return
	}
	if ctx.Err() != nil {
		return
	}
	request := r.Clone(ctx)
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	request.GetBody = nil
	request.Trailer = nil
	proxy := httputil.ReverseProxy{Rewrite: func(p *httputil.ProxyRequest) {
		p.SetURL(h.upstream)
		if h.cfg.LocalPostgRESTSocket != "" {
			p.Out.URL.Path = strings.TrimPrefix(p.Out.URL.Path, "/rest/v1")
			if p.Out.URL.Path == "" {
				p.Out.URL.Path = "/"
			}
			if p.Out.URL.RawPath != "" {
				p.Out.URL.RawPath = strings.TrimPrefix(p.Out.URL.RawPath, "/rest/v1")
			}
		}
		p.Out.Host = h.upstream.Host
		p.Out.Header.Del("Forwarded")
		p.Out.Header.Del("X-Forwarded-For")
		p.Out.Header.Del("X-Forwarded-Host")
		p.Out.Header.Del("X-Forwarded-Proto")
		p.Out.GetBody = nil
	}, Transport: h.transport, FlushInterval: -1, ErrorLog: log.New(io.Discard, "", 0), ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) { localError(w, 502, "upstream_outcome_unknown") }, ModifyResponse: func(resp *http.Response) error {
		failed = resp.StatusCode >= 400
		overloaded = resp.StatusCode == 429 || resp.StatusCode == 503 || resp.StatusCode == 504
		if resp.StatusCode == 101 {
			return errors.New("upgrade unsupported")
		}
		for k := range resp.Header {
			if strings.HasPrefix(strings.ToLower(k), "access-control-") {
				resp.Header.Del(k)
			}
		}
		if resp.Body == http.NoBody {
			complete.Store(true)
		} else {
			resp.Body = &completionBody{ReadCloser: resp.Body, complete: &complete}
		}
		return nil
	}}
	forwarded = true
	proxy.ServeHTTP(w, request)
}

type completionBody struct {
	io.ReadCloser
	complete *atomic.Bool
}

func (b *completionBody) Read(p []byte) (int, error) {
	n, e := b.ReadCloser.Read(p)
	if e == io.EOF {
		b.complete.Store(true)
	}
	return n, e
}
func localError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Pooltune-Error", code)
	if status == 429 || status == 503 {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": "POOLTUNE_" + strings.ToUpper(code), "message": code})
}
