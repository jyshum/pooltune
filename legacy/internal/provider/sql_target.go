package provider

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"strings"
)

// VerifySQLTarget binds the TLS name and authenticated tenant to the metrics resource.
func (a *Adapter) VerifySQLTarget(ctx context.Context, host, tlsName, user string) error {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	tlsName = strings.TrimSuffix(strings.ToLower(tlsName), ".")
	switch a.cfg.Kind {
	case Azure:
		want := strings.ToLower(a.cfg.Instance) + ".postgres.database.azure.com"
		if host == want && tlsName == want {
			return nil
		}
	case Supabase:
		direct := "db." + a.cfg.Project + ".supabase.co"
		if host == direct && tlsName == direct {
			return nil
		}
		if host == tlsName && strings.HasSuffix(host, ".pooler.supabase.com") && strings.HasSuffix(user, "."+a.cfg.Project) {
			return nil
		}
	case Google:
		raw, _, e := a.request(ctx, "GET", a.googleBase()+"/instances/"+url.PathEscape(a.cfg.Instance), nil)
		if e != nil {
			return e
		}
		var v struct {
			Name, Project, DNSName string
			IPAddresses            []struct{ Type, IPAddress string }
		}
		if json.Unmarshal(raw, &v) != nil || v.Name != a.cfg.Instance || v.Project != a.cfg.Project {
			return ErrAPI
		}
		want := strings.TrimSuffix(strings.ToLower(v.DNSName), ".")
		if want == "" || tlsName != want {
			return ErrInvalid
		}
		if host == want {
			return nil
		}
		for _, ip := range v.IPAddresses {
			if ip.Type == "PRIMARY" && net.ParseIP(host) != nil && host == ip.IPAddress {
				return nil
			}
		}
	}
	return ErrInvalid
}
