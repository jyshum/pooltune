package main

import (
	"bytes"
	"os"
	"path/filepath"
	"pooltune/internal/admin"
	"pooltune/internal/durable"
	"pooltune/internal/store"
	"strings"
	"testing"
	"time"
)

func TestOfflineOwnerRecoveryRequiresExclusiveControlAndPrivatePassword(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	db, e := store.OpenStore(filepath.Join(dir, "control.db"))
	if e != nil {
		t.Fatal(e)
	}
	claim := strings.Repeat("c", 43)
	old := "old-owner-password"
	next := "new-owner-password"
	auth, e := admin.NewAuth(db, claim, time.Now().Add(time.Hour), time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	session, e := auth.Claim(claim, old)
	if e != nil {
		t.Fatal(e)
	}
	db.Close()
	secrets := t.TempDir()
	os.Chmod(secrets, 0700)
	password := filepath.Join(secrets, "new-password")
	os.WriteFile(password, []byte(next+"\n"), 0600)
	args := []string{"reset-password", "-control-directory", dir, "-secret-root", secrets, "-password-file", password}
	var out bytes.Buffer
	lock, e := durable.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	if e = run(args, &out); e == nil {
		t.Fatal("recovery while control owns directory")
	}
	lock.Close()
	os.Chmod(password, 0644)
	if e = run(args, &out); e == nil {
		t.Fatal("public password file accepted")
	}
	os.Chmod(password, 0600)
	if e = run(args, &out); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(out.String(), next) || strings.Contains(out.String(), old) || strings.Contains(out.String(), claim) {
		t.Fatal("secret printed")
	}
	db, e = store.OpenStore(filepath.Join(dir, "control.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	auth, e = admin.NewAuth(db, claim, time.Now().Add(time.Hour), time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = auth.Session(session.Token); e == nil {
		t.Fatal("old session survived")
	}
	if _, e = auth.Login(old); e == nil {
		t.Fatal("old password survived")
	}
	if _, e = auth.Login(next); e != nil {
		t.Fatal("new password unusable")
	}
	if _, e = auth.Claim(claim, next); e == nil {
		t.Fatal("setup reopened")
	}
}
func TestOfflineClaimReplacementWithoutPrintingOrOverwriting(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	secretDir := t.TempDir()
	os.Chmod(secretDir, 0700)
	db, e := store.OpenStore(filepath.Join(dir, "control.db"))
	if e != nil {
		t.Fatal(e)
	}
	old := strings.Repeat("o", 43)
	_, e = admin.NewAuth(db, old, time.Now().Add(time.Minute), time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	db.Close()
	newFile := filepath.Join(secretDir, "claim")
	args := []string{"replace-claim", "-control-directory", dir, "-claim-file", newFile, "-valid-for", "1h"}
	var out bytes.Buffer
	if e = run(args, &out); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(newFile)
	if e != nil {
		t.Fatal(e)
	}
	token := strings.TrimSpace(string(raw))
	info, _ := os.Stat(newFile)
	if len(token) != 43 || token == old || info.Mode().Perm() != 0600 || strings.Contains(out.String(), token) {
		t.Fatal("unsafe replacement token")
	}
	if e = run(args, &out); e == nil {
		t.Fatal("token file overwritten")
	}
	db, e = store.OpenStore(filepath.Join(dir, "control.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	auth, e := admin.NewAuth(db, token, time.Now().Add(time.Hour), time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = auth.Claim(old, "owner-password-long"); e == nil {
		t.Fatal("old claim survived")
	}
	if _, e = auth.Claim(token, "owner-password-long"); e != nil {
		t.Fatal("replacement cannot claim")
	}
	missing := t.TempDir()
	os.Chmod(missing, 0700)
	if e = run([]string{"replace-claim", "-control-directory", missing, "-claim-file", filepath.Join(secretDir, "unused")}, &out); e == nil {
		t.Fatal("recovery initialized empty installation")
	}
	if _, e = os.Stat(filepath.Join(missing, "control.db")); !os.IsNotExist(e) {
		t.Fatal("new database created")
	}
}
