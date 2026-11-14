// pooltune-provider performs read-only control-plane discovery. It deliberately
// has no Apply path and cannot certify database readiness from API settings.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"pooltune/internal/provider"
	"pooltune/internal/secrets"
	"strings"
	"time"
)

type profile struct {
	Provider      provider.Kind `json:"provider"`
	Target        string        `json:"target"`
	Subscription  string        `json:"subscription,omitempty"`
	ResourceGroup string        `json:"resource_group,omitempty"`
	Project       string        `json:"project,omitempty"`
	Instance      string        `json:"instance,omitempty"`
	TokenFile     string        `json:"token_file,omitempty"`
}

func load(r io.Reader) (profile, error) {
	var p profile
	d := json.NewDecoder(io.LimitReader(r, 65537))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(&struct{}{}) != io.EOF {
		return p, provider.ErrInvalid
	}
	if p.TokenFile != "" && !filepath.IsAbs(p.TokenFile) {
		return p, provider.ErrInvalid
	}
	if p.Provider == provider.Supabase && p.TokenFile == "" {
		return p, provider.ErrInvalid
	}
	return p, nil
}
func token(ctx context.Context, p profile) (string, error) {
	if p.TokenFile != "" {
		reader, e := secrets.Open(filepath.Dir(p.TokenFile))
		if e != nil {
			return "", provider.ErrAPI
		}
		defer reader.Close()
		raw, e := reader.Read(p.TokenFile)
		if e != nil {
			return "", provider.ErrAPI
		}
		return strings.TrimSpace(string(raw)), nil
	}
	var cmd *exec.Cmd
	switch p.Provider {
	case provider.Azure:
		cmd = exec.CommandContext(ctx, "az", "account", "get-access-token", "--subscription", p.Subscription, "--resource", "https://management.azure.com/", "--query", "accessToken", "--output", "tsv")
	case provider.Google:
		cmd = exec.CommandContext(ctx, "gcloud", "auth", "print-access-token")
	default:
		return "", provider.ErrAPI
	}
	raw, e := cmd.Output()
	if e != nil || len(raw) > 65536 {
		return "", provider.ErrAPI
	}
	return strings.TrimSpace(string(raw)), nil
}
func discover(ctx context.Context, p profile) (provider.Observation, error) {
	a, e := provider.NewDiscovery(provider.Config{Kind: p.Provider, Target: provider.Target{ID: p.Target}, Subscription: p.Subscription, ResourceGroup: p.ResourceGroup, Project: p.Project, Instance: p.Instance, Timeout: 15 * time.Second, Freshness: time.Minute, Token: func(ctx context.Context) (string, error) { return token(ctx, p) }})
	if e != nil {
		return provider.Observation{}, e
	}
	return a.Observe(ctx, provider.Target{ID: p.Target})
}
func main() {
	path := flag.String("config", "", "Read-only provider profile JSON; credentials stay in private files or the authenticated cloud CLI")
	flag.Parse()
	f, e := os.Open(*path)
	if e != nil {
		fmt.Fprintln(os.Stderr, "Cannot read provider profile")
		os.Exit(1)
	}
	p, e := load(f)
	f.Close()
	if e != nil {
		fmt.Fprintln(os.Stderr, "Invalid read-only provider profile")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	obs, e := discover(ctx, p)
	report := map[string]any{"provider": p.Provider, "target": p.Target, "checked_at": time.Now().UTC(), "read_only": true, "database_readiness_verified": false, "automation_enabled": false}
	if e != nil {
		report["status"] = "discovery_blocked"
		report["reason"] = e.Error()
	} else {
		report["status"] = "configuration_read"
		if obs.Status == "discovery_incomplete" {
			report["status"] = obs.Status
			report["gaps"] = obs.Gaps
		}
		report["parameters"] = obs.Parameters
		report["configuration_fingerprint"] = obs.Version
		report["remaining"] = "Live database inventory, fresh provider pressure, full scope budget, safe change/readback and recovery verification"
	}
	json.NewEncoder(os.Stdout).Encode(report)
	if e != nil {
		os.Exit(1)
	}
}
