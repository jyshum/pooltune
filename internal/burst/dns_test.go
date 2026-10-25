package burst

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestPinnedDNSResolvesOnlyVerifiedHostname(t *testing.T) {
	pc, e := startPinnedDNS("db.example.test", "127.0.0.2")
	if e != nil {
		t.Fatal(e)
	}
	defer pc.Close()
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", pc.LocalAddr().String())
	}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ips, e := resolver.LookupHost(ctx, "db.example.test")
	if e != nil || len(ips) != 1 || ips[0] != "127.0.0.2" {
		t.Fatalf("DNS %v %v", ips, e)
	}
	if _, e = resolver.LookupHost(ctx, "other.example.test"); e == nil {
		t.Fatal("unapproved hostname resolved")
	}
}
