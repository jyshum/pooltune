//go:build integration

package provider

import (
	"context"
	"io"
	"net"
	"pooltune/internal/testpool"
	"strconv"
	"testing"
	"time"
)

func TestLocalPoolVersionBindsActualResource(t *testing.T) {
	var versions []string
	for i := 0; i < 2; i++ {
		ctx, a, app, m, _ := testpool.Start(t)
		p, e := NewLocalPool(LocalPoolConfig{Target: "same-label", Admin: a, Application: app, Monitor: m, Allocation: 8, Journal: &memoryJournal{}})
		if e != nil {
			t.Fatal(e)
		}
		o, e := p.Observe(ctx)
		p.Close()
		if e != nil {
			t.Fatal(e)
		}
		versions = append(versions, o.Version)
	}
	if versions[0] == versions[1] {
		t.Fatal("different clusters and poolers share retained authority version")
	}
}

func TestLocalPoolRejectsLoopbackForwarder(t *testing.T) {
	ctx, a, app, m, _ := testpool.Start(t)
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	go func() {
		for {
			incoming, e := listener.Accept()
			if e != nil {
				return
			}
			go func() {
				defer incoming.Close()
				upstream, e := net.DialTimeout("tcp", net.JoinHostPort(a.Host, strconv.Itoa(int(a.Port))), time.Second)
				if e != nil {
					return
				}
				defer upstream.Close()
				go func() { io.Copy(upstream, incoming); upstream.Close() }()
				io.Copy(incoming, upstream)
			}()
		}
	}()
	forwardedAdmin, forwardedApp := a.Copy(), app.Copy()
	forwardedAdmin.Port = uint16(listener.Addr().(*net.TCPAddr).Port)
	forwardedApp.Port = forwardedAdmin.Port
	p, e := NewLocalPool(LocalPoolConfig{Target: "forwarded", Admin: forwardedAdmin, Application: forwardedApp, Monitor: m, Allocation: 8, Journal: &memoryJournal{}})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	if _, e = p.Observe(context.WithoutCancel(ctx)); e == nil {
		t.Fatal("loopback TCP forwarder accepted as same-host pooler")
	}
}
