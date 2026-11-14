//go:build deployment

package tests

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This test builds and executes a native development package. It is deliberately
// separate from the still-unverified container install/restart release gate.
func TestNativePackageAllowlistManifestAndPrivateStartup(t *testing.T) {
	root, e := filepath.Abs("..")
	if e != nil {
		t.Fatal(e)
	}
	sentinel, e := os.CreateTemp(root, "package-secret-sentinel-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.Remove(sentinel.Name())
	public := filepath.Join(root, "ui", "public")
	if e = os.MkdirAll(public, 0700); e != nil {
		t.Fatal(e)
	}
	nested, e := os.MkdirTemp(public, "package-private-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(nested)
	marker := []byte("NEVER-SHIP-UNTRACKED-CREDENTIAL-SENTINEL-7a31d845")
	sentinel.Write(marker)
	sentinel.Close()
	if e = os.WriteFile(filepath.Join(nested, "private.key"), marker, 0600); e != nil {
		t.Fatal(e)
	}
	destination := filepath.Join(t.TempDir(), "pooltune")
	// Optional new output path retains the exact verified package for delivery.
	if requested := os.Getenv("POOLTUNE_PACKAGE_OUTPUT"); requested != "" {
		if !filepath.IsAbs(requested) {
			t.Fatal("package output must be absolute")
		}
		destination = requested
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, filepath.Join(root, "scripts/package-local.sh"), destination)
	build.Dir = root
	if output, e := build.CombinedOutput(); e != nil {
		t.Fatalf("native package: %v\n%s", e, output)
	}
	guide, err := os.ReadFile(filepath.Join(destination, "docs", "controller.md"))
	if err != nil || !bytes.Contains(guide, []byte("Service.Capacity.Guardrails")) {
		t.Fatal("delivered package lacks capacity setup instructions")
	}
	if e := exec.Command(filepath.Join(root, "scripts/package-local.sh"), destination).Run(); e == nil {
		t.Fatal("package output overwritten")
	}
	if e = filepath.WalkDir(destination, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(raw, marker) {
			t.Fatal("untracked public credential shipped in package directory")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	file, e := os.Open(destination + ".tar.gz")
	if e != nil {
		t.Fatal(e)
	}
	defer file.Close()
	gz, e := gzip.NewReader(file)
	if e != nil {
		t.Fatal(e)
	}
	defer gz.Close()
	entries := map[string][]byte{}
	tr := tar.NewReader(gz)
	for {
		header, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		name := strings.TrimPrefix(header.Name, "pooltune/")
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg || strings.Contains(name, "..") || strings.HasPrefix(name, "/") {
			t.Fatal("unsafe archive member")
		}
		if name != "SHA256SUMS" && name != "RELEASE.json" && !strings.HasPrefix(name, "bin/") && !strings.HasPrefix(name, "ui/") && !strings.HasPrefix(name, "sdk/") && !strings.HasPrefix(name, "docs/") {
			t.Fatalf("unapproved package member: %s", name)
		}
		b, e := io.ReadAll(tr)
		if e != nil {
			t.Fatal(e)
		}
		if bytes.Contains(b, marker) {
			t.Fatal("untracked credential shipped")
		}
		entries[name] = b
	}
	for _, required := range []string{"bin/pooltune", "bin/pooltune-owner", "bin/pooltune-tuner", "bin/pooltune-provider", "bin/pooltune-burst", "docs/burst-gateway.md", "docs/burst-gateway-results.md", "docs/quick-confirmation.md", "docs/managed-mvp-results.md", "docs/evidence/managed-mvp-2026-10-25/summary.json", "docs/evidence/quick-confirmation-2026-10-25/summary.json", "docs/background-tuner.md", "ui/index.html", "sdk/supabase-fetch.js", "sdk/supabase-fetch.d.ts", "docs/install.md", "docs/release-gates.md", "RELEASE.json", "SHA256SUMS"} {
		if len(entries[required]) == 0 {
			t.Fatalf("missing package member: %s", required)
		}
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(entries["SHA256SUMS"])), "\n") {
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) != 2 {
			t.Fatal("invalid manifest")
		}
		sum := sha256.Sum256(entries[parts[1]])
		if hex.EncodeToString(sum[:]) != parts[0] || seen[parts[1]] {
			t.Fatal("manifest mismatch")
		}
		seen[parts[1]] = true
	}
	if len(seen) != len(entries)-1 {
		t.Fatal("unmanifested member")
	}
	var release struct {
		Profile string
		Dirty   bool
		Commit  string
	}
	if json.Unmarshal(entries["RELEASE.json"], &release) != nil || release.Profile != "local-development" || len(release.Commit) != 40 {
		t.Fatal("unidentified package")
	}
	// Use the packaged executable, with no profile or secrets. Verify every mode is
	// live but unready and never exposes admin/admission in a fresh installation.
	for _, mode := range []string{"traffic", "control", "gateway"} {
		ln, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		addr := ln.Addr().String()
		ln.Close()
		cmd := exec.CommandContext(ctx, filepath.Join(destination, "bin/pooltune"), mode, "-listen", addr)
		if e = cmd.Start(); e != nil {
			t.Fatal(e)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		func() {
			defer func() {
				cmd.Process.Signal(os.Interrupt)
				select {
				case e := <-done:
					if e != nil {
						t.Errorf("package shutdown: %v", e)
					}
				case <-time.After(3 * time.Second):
					cmd.Process.Kill()
					<-done
					t.Error("package shutdown hung")
				}
			}()
			client := &http.Client{Timeout: 100 * time.Millisecond}
			defer client.CloseIdleConnections()
			deadline := time.Now().Add(3 * time.Second)
			for {
				r, e := client.Get("http://" + addr + "/healthz")
				if e == nil {
					r.Body.Close()
					if r.StatusCode == 200 {
						break
					}
				}
				if time.Now().After(deadline) {
					t.Fatal("packaged executable unavailable")
				}
				time.Sleep(10 * time.Millisecond)
			}
			for path, want := range map[string]int{"/readyz": 503, "/api/state": 404, "/rest/v1/items": 404} {
				r, e := client.Get("http://" + addr + path)
				if e != nil {
					t.Fatal(e)
				}
				r.Body.Close()
				if r.StatusCode != want {
					t.Fatalf("fresh %s %s status %d", mode, path, r.StatusCode)
				}
			}
		}()
	}
	t.Logf("Native package %s: complete SHA256 manifest, explicit payload allowlist, no untracked sentinel, packaged binaries run with private unconfigured startup and clean shutdown. Containers and configured traffic restart remain unverified.", release.Commit[:7])
}
