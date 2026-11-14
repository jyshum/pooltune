package store

import (
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOfflineAdminRecoveryIsAtomicAuditedAndPreservesLedger(t *testing.T) {
	s, e := OpenStore(filepath.Join(t.TempDir(), "control.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	claim := sha256.Sum256([]byte("original"))
	replacement := sha256.Sum256([]byte("replacement"))
	session := sha256.Sum256([]byte("session"))
	if e = s.InitAdmin(claim[:], time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	// Failed audit persistence must roll back a new claim, password and session revocation.
	if _, e = s.db.Exec(`CREATE TRIGGER fail_recovery BEFORE INSERT ON admin_audit WHEN NEW.kind IN ('password_recovered','claim_replaced') BEGIN SELECT RAISE(ABORT,'injected'); END`); e != nil {
		t.Fatal(e)
	}
	if e = s.ReplaceAdminClaim(replacement[:], time.Now().Add(time.Hour)); e == nil {
		t.Fatal("unaudited claim replacement")
	}
	old := []byte(strings.Repeat("x", 60))
	next := []byte(strings.Repeat("y", 60))
	if e = s.ClaimAdmin(claim[:], old, session[:], strings.Repeat("c", 43), time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	if e = s.RecoverAdminPassword(next); e == nil {
		t.Fatal("unaudited password recovery")
	}
	hash, e := s.AdminPasswordHash()
	if e != nil || string(hash) != string(old) {
		t.Fatal("failed recovery changed password")
	}
	if _, _, e = s.AdminSession(session[:]); e != nil {
		t.Fatal("failed recovery revoked session")
	}
	if _, e = s.db.Exec("DROP TRIGGER fail_recovery"); e != nil {
		t.Fatal(e)
	}
	if e = s.RecoverAdminPassword(next); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.AdminSession(session[:]); e == nil {
		t.Fatal("recovered password retained old session")
	}
	if e = s.ReplaceAdminClaim(replacement[:], time.Now().Add(time.Hour)); e == nil {
		t.Fatal("reopened claimed setup")
	}
	var count int
	if e = s.db.QueryRow("SELECT count(*) FROM admin_audit WHERE kind='password_recovered'").Scan(&count); e != nil || count != 1 {
		t.Fatal("missing recovery audit")
	}
	if e = s.db.QueryRow("SELECT count(*) FROM admin_audit WHERE kind='claimed'").Scan(&count); e != nil || count != 1 {
		t.Fatal("history erased")
	}
}

func TestRecoveryOpenNeverInitializesUnrelatedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unrelated.db")
	db, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if _, e = db.Exec("CREATE TABLE unrelated(value TEXT)"); e != nil {
		t.Fatal(e)
	}
	os.Chmod(path, 0600)
	if recovered, e := OpenExistingControlStore(path); e == nil {
		recovered.Close()
		t.Fatal("unrelated database accepted")
	}
	var n int
	if e = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name<>'unrelated'").Scan(&n); e != nil || n != 0 {
		t.Fatal("recovery initialized unrelated database")
	}
}
