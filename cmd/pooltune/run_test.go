package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHealthOnlyModesWithoutCredentials(t *testing.T) {
	for _, mode := range []string{"traffic", "control", "gateway"} {
		t.Run(mode, func(t *testing.T) {
			ln, err := listenLocal("127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- serve(ctx, ln, mode) }()
			client := &http.Client{Timeout: time.Second}
			for path, want := range map[string]int{"/healthz": 200, "/readyz": 503, "/": 404, "/admit": 404, "/admin": 404} {
				resp, err := client.Get("http://" + ln.Addr().String() + path)
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != want {
					t.Fatalf("%s: got %d, want %d", path, resp.StatusCode, want)
				}
				if strings.Contains(string(body), "ready") && path == "/healthz" {
					t.Fatal("health implies readiness")
				}
			}
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("server did not stop")
			}
		})
	}
}

func TestListenLocalRejectsNonLoopback(t *testing.T) {
	for _, addr := range []string{":0", "0.0.0.0:0", "[::]:0", "localhost:0", "8.8.8.8:0"} {
		ln, err := listenLocal(addr)
		if ln != nil {
			ln.Close()
		}
		if err == nil {
			t.Fatalf("accepted %q", addr)
		}
	}
}

func TestRunRejectsInvalidModeAndConfig(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, args := range [][]string{{"bad"}, {"traffic", "-config", "/does/not/exist"}} {
		if err := run(ctx, args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
