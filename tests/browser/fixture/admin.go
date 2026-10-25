//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"pooltune/internal/admin"
	"pooltune/internal/store"
	"strings"
	"sync"
	"time"
)

// This fixture exercises real auth/HTTP/UI with synthetic provider observations.
// It is not evidence that a real database connection or policy was activated.
type adminBackend struct {
	mu      sync.Mutex
	number  int
	state   map[string]any
	history []map[string]string
}

func (b *adminBackend) Read(_ context.Context, resource string) (any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if resource == "revisions" {
		return map[string]any{"items": append([]map[string]string{}, b.history...)}, nil
	}
	if resource == "runs" {
		return map[string]any{"items": []any{}}, nil
	}
	raw, _ := json.Marshal(b.state)
	var copied map[string]any
	json.Unmarshal(raw, &copied)
	return copied, nil
}
func (b *adminBackend) Execute(_ context.Context, action string, c admin.Command) (any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if c.ExpectedRevision != b.state["revision"] {
		return nil, admin.ErrConflict
	}
	setup := b.state["setup"].(map[string]any)
	switch action {
	case "setup/draft":
		if c.Setup == nil || c.Setup.ConnectionRef != "fixture-db" {
			return nil, admin.ErrDenied
		}
		b.state["target"] = c.Setup.TargetID
		b.state["provider"] = c.Setup.Provider
		b.state["path"] = c.Setup.Path
	case "setup/verify":
		if b.state["target"] == "" {
			return nil, admin.ErrDenied
		}
		setup["verified"] = true
	case "setup/observe":
		if setup["verified"] != true {
			return nil, admin.ErrDenied
		}
		setup["observed"] = true
		setup["matched_percent"] = 100
		setup["groups"] = []map[string]any{{"workload_id": "w1", "label": "Interactive lookup", "class": "normal", "locked": false, "source": "measured", "allowed_classes": []string{"critical", "normal", "background"}}}
	case "setup/approve":
		if setup["observed"] != true || len(c.Groups) != 1 || c.Groups[0].WorkloadID != "w1" {
			return nil, admin.ErrDenied
		}
		setup["approved"] = true
		b.state["ready"] = true
	case "freeze":
		b.state["frozen"] = true
		b.state["safety"] = map[string]any{"stopped": true, "resume_ready": false, "start_capacity": 2}
	case "resume":
		b.state["frozen"] = false
		b.state["safety"] = map[string]any{"stopped": true, "resume_ready": true, "start_capacity": 2}
	case "safety-resume":
		proof, ok := b.state["safety"].(map[string]any)
		if !ok || proof["resume_ready"] != true || b.state["frozen"] == true {
			return nil, admin.ErrDenied
		}
		if proof["stopped"] == true {
			// Synthetic reduced-ceiling state exercises the second UI action.
			b.state["safety"] = map[string]any{"restricted": true, "stopped": false, "resume_ready": true, "start_capacity": 2}
		} else {
			b.state["safety"] = map[string]any{"restricted": false, "stopped": false, "resume_ready": false, "start_capacity": 0}
		}
	case "revert":
		if c.Revision == "" {
			return nil, admin.ErrDenied
		}
	default:
		return nil, admin.ErrDenied
	}
	b.number++
	revision := fmt.Sprintf("r%d", b.number)
	b.state["revision"] = revision
	b.history = append(b.history, map[string]string{"id": revision, "actor": "owner", "status": "confirmed", "at": time.Now().Format(time.RFC3339), "reason": action + " (controlled fixture)"})
	return map[string]string{"revision": revision, "status": "confirmed"}, nil
}
func startAdminFixture() {
	dir, e := os.MkdirTemp("", "pooltune-admin-browser-")
	if e != nil {
		log.Fatal(e)
	}
	db, e := store.OpenStore(filepath.Join(dir, "control.db"))
	if e != nil {
		log.Fatal(e)
	}
	auth, e := admin.NewAuth(db, strings.Repeat("x", 43), time.Now().Add(time.Hour), time.Hour)
	if e != nil {
		log.Fatal(e)
	}
	b := &adminBackend{number: 1, state: map[string]any{"revision": "r1", "target": "", "provider": "local", "path": "postgres", "ready": false, "frozen": false, "setup": map[string]any{"verified": false, "observed": false, "approved": false, "groups": []any{}, "matched_percent": 0, "ai_available": false}, "limits": map[string]int{"effective": 20, "physical": 25, "stable": 20, "candidate": 0}, "metrics": map[string]any{"p95_ms": 2.4, "error_rate": 0, "queued": 0}, "gates": []string{"Controlled browser fixture. No real provider observation or traffic activation."}}}
	handler, e := admin.NewHandler(admin.Config{Origin: "http://127.0.0.1:12163", LocalHTTP: true}, auth, b, os.DirFS("ui/dist"))
	if e != nil {
		log.Fatal(e)
	}
	server := &http.Server{Addr: "127.0.0.1:12163", Handler: handler, ReadHeaderTimeout: time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 35 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		if e := server.ListenAndServe(); e != nil {
			log.Fatal(e)
		}
	}()
}
