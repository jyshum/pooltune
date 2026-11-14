package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"pooltune/internal/control"
	"pooltune/internal/domain"
	"pooltune/internal/rollout"
	"strings"
	"testing"
	"time"
)

func TestControlProfileRequiresRealPinnedCalibrationFile(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	c := ControlConfig{Schema: "local-control-1", Directory: dir, Socket: filepath.Join(dir, "traffic.sock"), Service: control.ServiceConfig{Target: "db", Path: domain.PathPostgres, Tick: time.Second, Window: 30 * time.Second, MaxEvidenceAge: time.Second, ResourceSource: "local-host"}}
	encode := func() []byte { b, _ := json.Marshal(c); return b }
	if _, err := LoadControl(bytes.NewReader(encode())); err != nil {
		t.Fatal("observation-only profile rejected", err)
	}
	c.Service.Evaluation = &rollout.EvaluationProfile{Validated: true, EvidenceSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if _, err := LoadControl(bytes.NewReader(encode())); err == nil {
		t.Fatal("boolean substituted for calibration artifact")
	}
	data := []byte("controlled loader test artifact, not a calibrated production profile")
	c.CalibrationFile = filepath.Join(dir, "calibration.json")
	os.WriteFile(c.CalibrationFile, data, 0600)
	if _, err := LoadControl(bytes.NewReader(encode())); err == nil {
		t.Fatal("digest mismatch accepted")
	}
	hash := sha256.Sum256(data)
	c.Service.Evaluation.EvidenceSHA256 = hex.EncodeToString(hash[:])
	if _, err := LoadControl(bytes.NewReader(encode())); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadControl(bytes.NewReader(append(encode(), []byte(" {}")...))); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}

func TestControlAdminMustBePrivateAndSeparate(t *testing.T) {
	dir := t.TempDir()
	c := ControlConfig{Schema: "local-control-1", Directory: dir, Socket: filepath.Join(dir, "traffic.sock"), Service: control.ServiceConfig{ResourceSource: "local-host"}, Admin: &AdminConfig{Listen: "0.0.0.0:9000", Origin: "http://127.0.0.1:9000", Assets: dir, SecretRoot: dir, ClaimFile: filepath.Join(dir, "claim"), SessionLifetime: time.Hour, ClaimExpires: time.Now().Add(time.Hour)}}
	b, _ := json.Marshal(c)
	if _, err := LoadControl(bytes.NewReader(b)); err == nil {
		t.Fatal("public admin binding accepted")
	}
	c.Admin.Listen = "127.0.0.1:9000"
	b, _ = json.Marshal(c)
	if _, err := LoadControl(bytes.NewReader(b)); err != nil {
		t.Fatal(err)
	}
	c.Admin.Origin = "http://127.0.0.1:9001"
	b, _ = json.Marshal(c)
	if _, err := LoadControl(bytes.NewReader(b)); err == nil {
		t.Fatal("mismatched origin accepted")
	}
}

func TestCapacityProfileRequiresPinnedArtifact(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	c := ControlConfig{Schema: "local-control-1", Directory: dir, Socket: filepath.Join(dir, "traffic.sock"), Service: control.ServiceConfig{Target: "db", Path: domain.PathPostgres, ResourceSource: "local-host", Capacity: &control.CapacityProfile{EvidenceSHA256: strings.Repeat("a", 64)}}}
	load := func() error { b, _ := json.Marshal(c); _, err := LoadControl(bytes.NewReader(b)); return err }
	if load() == nil {
		t.Fatal("capacity enabled without artifact")
	}
	data := []byte("controlled loader artifact; not production calibration")
	c.CapacityCalibrationFile = filepath.Join(dir, "capacity.json")
	os.WriteFile(c.CapacityCalibrationFile, data, 0600)
	if load() == nil {
		t.Fatal("wrong capacity artifact accepted")
	}
	sum := sha256.Sum256(data)
	c.Service.Capacity.EvidenceSHA256 = hex.EncodeToString(sum[:])
	if err := load(); err != nil {
		t.Fatal(err)
	}
	c.Service.Path = domain.PathREST
	if load() == nil {
		t.Fatal("REST growth without explicit HTTP envelope accepted")
	}
	c.Service.Capacity.Guardrails.HTTPEnvelope = 10
	if err := load(); err != nil {
		t.Fatal("pinned REST envelope rejected", err)
	}
}
