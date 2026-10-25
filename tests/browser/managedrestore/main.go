//go:build integration

// Isolated UI fixture. Synthetic health, real controller/journal/authentication.
// Uses IPv6 loopback so its session cookie cannot replace the judging demo cookie.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"pooltune/internal/admin"
	"pooltune/internal/burst"
	"pooltune/internal/durable"
	"pooltune/internal/store"
	"strings"
	"syscall"
	"time"
)

func main() {
	assets := flag.String("assets", "", "separate UI build directory")
	listen := flag.String("listen", "[::1]:14285", "isolated loopback listener")
	kind := flag.String("provider", "azure", "azure, google or supabase")
	flag.Parse()
	if *kind != "azure" && *kind != "google" && *kind != "supabase" {
		log.Fatal("unsupported fixture provider")
	}
	if *assets == "" {
		log.Fatal("explicit assets directory required")
	}
	dir, e := os.MkdirTemp("", "pooltune-restore-preview-")
	if e != nil {
		log.Fatal(e)
	}
	defer os.RemoveAll(dir)
	files, e := durable.Open(dir)
	if e != nil {
		log.Fatal(e)
	}
	defer files.Close()
	c, e := burst.NewController(burst.Config{Target: "Simulated managed restore preview", Base: 4, Ceiling: 8, Queue: 16, QueueWait: time.Second, PressureSource: *kind + ":fixture", PressureMaxAge: 75 * time.Second}, func(s burst.State) error {
		raw, e := json.Marshal(s)
		if e != nil {
			return e
		}
		return files.Save("state.json", raw)
	})
	if e != nil {
		log.Fatal(e)
	}
	update := func() {
		now := time.Now()
		c.Update(burst.Health{At: now, PressureAt: now, Source: *kind + ":fixture", CPU: .2, Memory: .3, Warm: 8, BudgetVerified: true})
		c.Tick(now)
	}
	update()
	if e = c.Resume(time.Now()); e != nil {
		log.Fatal(e)
	}
	// A genuine queued acquire makes the controller record a reserve expansion.
	var releases []func()
	for i := 0; i < 4; i++ {
		p, e := c.Scheduler.Acquire(context.Background(), c.Classify("fixture", "db", fmt.Sprint(i)))
		if e != nil {
			log.Fatal(e)
		}
		releases = append(releases, func() { c.Scheduler.Release(p, 0) })
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		p, e := c.Scheduler.Acquire(context.Background(), c.Classify("fixture", "db", "queued"))
		if e == nil {
			c.Scheduler.Release(p, 0)
		}
	}()
	deadline := time.Now().Add(time.Second)
	for c.Scheduler.Stats().Queued == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	update()
	<-done
	for _, release := range releases {
		release()
	}
	c.Freeze()
	update()
	if e = c.Resume(time.Now()); e != nil {
		log.Fatal(e)
	}
	db, e := store.OpenStore(filepath.Join(dir, "auth.db"))
	if e != nil {
		log.Fatal(e)
	}
	defer db.Close()
	auth, e := admin.NewAuth(db, strings.Repeat("p", 43), time.Now().Add(time.Hour), time.Hour)
	if e != nil {
		log.Fatal(e)
	}
	if _, e = auth.Claim(strings.Repeat("p", 43), "managed-restore-preview-password"); e != nil {
		log.Fatal(e)
	}
	listener, e := net.Listen("tcp", *listen)
	if e != nil {
		log.Fatal(e)
	}
	origin := "http://" + listener.Addr().String()
	handler, e := admin.NewHandler(admin.Config{Origin: origin, LocalHTTP: true}, auth, burst.DashboardBackend{Controller: c}, os.DirFS(*assets))
	if e != nil {
		log.Fatal(e)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				update()
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		if e = server.Serve(listener); e != nil && e != http.ErrServerClosed {
			log.Print(e)
			cancel()
		}
	}()
	fmt.Println("Isolated simulated restore preview:", origin)
	<-ctx.Done()
	c.Shutdown()
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer closeCancel()
	_ = server.Shutdown(closeCtx)
}
