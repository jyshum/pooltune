package control

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"pooltune/internal/domain"
	"pooltune/internal/rollout"
	"time"
)

type WindowCollector interface {
	BeginWindow(string, time.Time, time.Duration, time.Time) error
	Snapshot(time.Time) (rollout.Window, error)
}
type WindowRequest struct {
	Revision    domain.RevisionID
	Duration    time.Duration
	PreviousEnd time.Time
}
type WindowSnapshot struct {
	Live      []rollout.LiveWorkload
	Resources domain.Resources
	Revision  domain.RevisionID
	At        time.Time
	Window    rollout.Window
}

func (e *Engine) beginWindow(r WindowRequest) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cfg.Measurements == nil || !e.ready.Load() || e.auditGap || e.state.Active.Revision != r.Revision || !e.s.Drained() {
		return ErrRecovery
	}
	if err := e.cfg.Measurements.BeginWindow(string(r.Revision), time.Now(), r.Duration, r.PreviousEnd); err != nil {
		return err
	}
	e.measurementRevision = r.Revision
	return nil
}
func (e *Engine) readWindow() (WindowSnapshot, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cfg.Measurements == nil || !e.ready.Load() || e.measurementRevision != e.state.Active.Revision {
		return WindowSnapshot{}, ErrRecovery
	}
	now := time.Now()
	window, err := e.cfg.Measurements.Snapshot(now)
	if err != nil {
		window = rollout.Window{}
	}
	live := []rollout.LiveWorkload{}
	for name, w := range e.s.Stats().ByWorkload {
		live = append(live, rollout.LiveWorkload{Workload: name, Active: w.Active, Queued: w.Queued, Uncertain: w.Uncertain, OldestActive: w.OldestActiveAge, OldestQueued: w.OldestQueuedAge})
	}
	resources := domain.Resources{}
	if e.cfg.Resources != nil {
		resources = e.cfg.Resources()
	}
	return WindowSnapshot{Revision: e.measurementRevision, At: now, Window: window, Resources: resources, Live: live}, nil
}
func (e *Engine) measurementRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /measurement", func(w http.ResponseWriter, r *http.Request) {
		var input WindowRequest
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2048))
		if err != nil || decode(data, &input) != nil {
			http.Error(w, "invalid measurement request", 400)
			return
		}
		if err = e.beginWindow(input); err != nil {
			http.Error(w, "measurement unavailable", 409)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /measurement", func(w http.ResponseWriter, r *http.Request) {
		v, err := e.readWindow()
		if err != nil {
			http.Error(w, "measurement unavailable", 409)
			return
		}
		json.NewEncoder(w).Encode(v)
	})
}
func (c *Client) BeginWindow(ctx context.Context, r WindowRequest) error {
	return c.call(ctx, "POST", "/measurement", r, nil)
}
func (c *Client) ReadWindow(ctx context.Context) (WindowSnapshot, error) {
	var out WindowSnapshot
	err := c.call(ctx, "GET", "/measurement", nil, &out)
	return out, err
}
