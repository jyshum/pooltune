//go:build integration

package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"pooltune/internal/admin"
	"pooltune/internal/burst"
	"pooltune/internal/store"
	"pooltune/internal/telemetry"
	"strings"
	"time"
)

func startBurstFixture() {
	dir, e := os.MkdirTemp("", "pooltune-burst-browser-")
	if e != nil {
		log.Fatal(e)
	}
	db, e := store.OpenStore(dir + "/control.db")
	if e != nil {
		log.Fatal(e)
	}
	c, e := burst.NewController(burst.Config{Target: "controlled-managed-fixture", Base: 4, Ceiling: 8, Queue: 16, QueueWait: time.Second, PressureSource: "azure:fixture", PressureMaxAge: 135 * time.Second}, nil)
	if e != nil {
		log.Fatal(e)
	}
	traffic := demoTransactions()
	update := func() {
		now := time.Now()
		c.Update(burst.Health{Transactions: traffic(), At: now, PressureAt: now.Add(-80 * time.Second), Source: "azure:fixture", CPU: .2, Memory: .3, Warm: 8, BudgetVerified: true})
		c.Tick(now)
	}
	update()
	if e = c.Resume(time.Now()); e != nil {
		log.Fatal(e)
	}
	auth, e := admin.NewAuth(db, strings.Repeat("m", 43), time.Now().Add(time.Hour), time.Hour)
	if e != nil {
		log.Fatal(e)
	}
	history, _ := telemetry.WrapEnv(context.Background(), burst.DashboardBackend{Controller: c}, "managed")
	handler, e := admin.NewHandler(admin.Config{Origin: "http://127.0.0.1:12165", LocalHTTP: true}, auth, history, os.DirFS("ui/dist"))
	if e != nil {
		log.Fatal(e)
	}
	go func() {
		for {
			update()
			time.Sleep(500 * time.Millisecond)
		}
	}()
	go func() { log.Fatal(http.ListenAndServe("127.0.0.1:12165", handler)) }()
}
