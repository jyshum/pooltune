package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"pooltune/internal/burst"
	"syscall"
	"time"
)

func main() {
	path := flag.String("config", "", "Absolute burst-gateway-1 profile path")
	resume := flag.Bool("resume", false, "Explicitly enable admission after verified warm startup")
	dashboard := flag.String("dashboard-listen", "", "Optional private dashboard address, e.g. 127.0.0.1:8081")
	assets := flag.String("assets", "", "Absolute packaged ui directory for the private dashboard")
	flag.Parse()
	if e := run(*path, *resume, *dashboard, *assets); e != nil {
		fmt.Fprintln(os.Stderr, "Pooltune burst gateway stopped: private configuration, capacity verification or retained state requires attention.")
		os.Exit(1)
	}
}
func run(path string, resume bool, dashboardListen, assets string) error {
	if (dashboardListen == "") != (assets == "") {
		return burst.ErrUnsafe
	}
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	p, e := burst.LoadProfile(f)
	f.Close()
	if e != nil {
		return e
	}
	s, e := p.Settings()
	if e != nil {
		return e
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, e := burst.Start(ctx, s)
	if e != nil {
		return e
	}
	var dashboardErrors <-chan error
	if dashboardListen != "" {
		dashboard, e := r.StartDashboard(dashboardListen, assets, p.SecretRoot)
		if e != nil {
			r.Close()
			return e
		}
		defer dashboard.Close()
		dashboardErrors = dashboard.Errors
		fmt.Printf("Private managed dashboard: %s\nClaim token: private state directory / claim.token\n", dashboard.URL)
	}
	if resume {
		if e = r.Controller.Resume(time.Now()); e != nil {
			r.Close()
			return e
		}
	}
	fmt.Printf("Experimental private PostgreSQL endpoint: %s\nWarm reserve is retained. No automatic backup route.\n", p.Listen)
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	closing := false
	enc := json.NewEncoder(os.Stdout)
	for {
		select {
		case <-dashboardErrors:
			dashboardErrors = nil
			closing = true
			r.Controller.Freeze()
			fmt.Println("Dashboard unavailable; admission frozen while active work drains.")
		case <-signals:
			closing = true
			r.Controller.Freeze()
			fmt.Println("Admission frozen; waiting for active transactions. Unknown outcomes require manual reconciliation.")
		case <-tick.C:
			if closing {
				if r.Close() == nil {
					return nil
				}
			} else {
				_ = enc.Encode(r.Controller.State())
			}
		}
	}
}
