//go:build integration

package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"pooltune/internal/admin"
	"pooltune/internal/domain"
	"pooltune/internal/durable"
	"pooltune/internal/provider"
	"pooltune/internal/store"
	"pooltune/internal/telemetry"
	"pooltune/internal/tuning"
	"strings"
	"time"
)

type browserPool struct{ traffic func() *provider.Transactions }

func (p browserPool) Observe(context.Context) (provider.LocalPoolStatus, error) {
	return provider.LocalPoolStatus{Transactions: p.traffic(), Identity: "controlled-browser-resource", Version: "controlled-browser-fixture", Target: 4, Working: 4, Idle: 2, Slots: 100, At: time.Now()}, nil
}
func (browserPool) Apply(context.Context, string, string, int) error { return provider.ErrUnsupported }
func startTuningFixture() {
	dir, e := os.MkdirTemp("", "pooltune-tuning-browser-")
	if e != nil {
		log.Fatal(e)
	}
	files, e := durable.Open(dir)
	if e != nil {
		log.Fatal(e)
	}
	db, e := store.OpenStore(dir + "/control.db")
	if e != nil {
		log.Fatal(e)
	}
	service, e := tuning.New("controlled-local-pool", tuning.Bounds{Minimum: 2, Maximum: 8, CPUGrow: .6, CPUBackoff: .8, MemoryGrow: .7, MemoryBackoff: .9}, browserPool{traffic: demoTransactions()}, db, files)
	if e != nil {
		log.Fatal(e)
	}
	auth, e := admin.NewAuth(db, strings.Repeat("t", 43), time.Now().Add(time.Hour), time.Hour)
	if e != nil {
		log.Fatal(e)
	}
	history, _ := telemetry.WrapEnv(context.Background(), service, "local")
	handler, e := admin.NewHandler(admin.Config{Origin: "http://127.0.0.1:12164", LocalHTTP: true}, auth, history, os.DirFS("ui/dist"))
	if e != nil {
		log.Fatal(e)
	}
	go func() {
		for {
			now := time.Now()
			service.Tick(context.Background(), now, domain.Resources{At: now, CPU: .2, Memory: .3, Source: "local-host"})
			time.Sleep(time.Second)
		}
	}()
	go func() {
		if e := http.ListenAndServe("127.0.0.1:12164", handler); e != nil {
			log.Fatal(e)
		}
	}()
}
