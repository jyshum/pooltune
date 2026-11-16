//go:build integration

package hostedapp

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"pooltune/internal/hostedapp/auth"
	"pooltune/internal/hostedquery"
	"pooltune/internal/testpg"
)

func TestEnrollmentEndpointStoresOnlyFreshOwnerSelectedCatalogIdentity(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t)
	poolCfg, err := pgxpool.ParseConfig(cfg.ConnString())
	if err != nil {
		t.Fatal(err)
	}
	poolCfg.ConnConfig = cfg
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	app, err := New(ctx, pool, Config{Origin: "https://preview.example.com", InviteCode: "private-enroll-test-code", Key: bytes.Repeat([]byte{18}, 32), Sampling: hostedquery.SamplingPolicy{Interval: time.Minute, Window: 5 * time.Minute}, QueueInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	user, session, err := app.accounts.Register(ctx, "enroll@example.com", "a sufficiently long test password")
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := app.accounts.Register(ctx, "other@example.com", "another sufficiently long password")
	if err != nil {
		t.Fatal(err)
	}
	create := func(ref string) projectRecord {
		t.Helper()
		id, err := app.projects.create(ctx, user.ID, "Enrolled project", ref)
		if err != nil {
			t.Fatal(err)
		}
		row, err := app.projects.find(ctx, user.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		sealed, err := app.vault.seal(row, projectSecrets{Automatic: true, RepairPassword: "never-leak-this-password", Capabilities: []string{"memory", "cron"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := app.projects.activate(ctx, row, sealed); err != nil {
			t.Fatal(err)
		}
		row, err = app.projects.find(ctx, user.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	row, second := create("abcdefghijklmnopqrst"), create("bcdefghijklmnopqrstu")
	db, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	if _, err := db.Exec(ctx, `CREATE FUNCTION public.enroll_report(integer) RETURNS int LANGUAGE sql AS $$SELECT $1$$`); err != nil {
		t.Fatal(err)
	}
	var selectedJobs []repairEnrollmentTarget
	read := func(context.Context, projectRecord, projectSecrets) (repairEnrollmentCatalog, error) {
		catalog, err := readRepairEnrollmentCatalog(ctx, db)
		catalog.Jobs = selectedJobs
		return catalog, err
	}
	catalog, err := read(ctx, row, projectSecrets{})
	if err != nil {
		t.Fatal(err)
	}
	var target repairEnrollmentTarget
	for _, c := range catalog.Functions {
		if c.Name == "enroll_report" {
			target = c
		}
	}
	if target.ObjectOID == 0 {
		t.Fatal("test function not discovered")
	}
	choice := func(row projectRecord, target repairEnrollmentTarget) string {
		t.Helper()
		token, err := app.vault.enrollmentChoice(row, target, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	post := func(as auth.User, project projectRecord, fields url.Values) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", "https://preview.example.com/app/projects/"+project.ID+"/repair-enrollment", strings.NewReader(fields.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.SetPathValue("id", project.ID)
		w := httptest.NewRecorder()
		app.changeRepairEnrollmentWith(w, r, as, read)
		if strings.Contains(w.Body.String(), "never-leak") {
			t.Fatal("credential leaked")
		}
		return w
	}
	fields := url.Values{"mode": {"enroll"}, "target": {choice(row, target)}}
	if w := post(other, row, fields); w.Code != 404 {
		t.Fatalf("other account allowed: %d", w.Code)
	}
	if w := post(user, second, fields); w.Code != 409 {
		t.Fatalf("other project accepted selection: %d", w.Code)
	}
	if w := post(user, row, fields); w.Code != 303 {
		t.Fatalf("enroll failed: %d %s", w.Code, w.Body.String())
	}
	updated, err := app.projects.find(ctx, user.ID, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := app.vault.open(updated, updated.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Epoch != row.Epoch+1 || len(secrets.Enrollments) != 1 || secrets.Enrollments[0].Target != target || secrets.Enrollments[0].GrantedAtEpoch != updated.Epoch {
		t.Fatalf("target/epoch not saved: %+v", secrets.Enrollments)
	}
	if bytes.Contains(updated.Secret, []byte("enroll_report")) {
		t.Fatal("enrollment stored without encryption")
	}
	if w := post(user, row, fields); w.Code != 409 {
		t.Fatalf("stale selection replayed: %d", w.Code)
	}
	fresh := url.Values{"mode": {"enroll"}, "target": {choice(updated, target)}}
	if _, err := db.Exec(ctx, `CREATE OR REPLACE FUNCTION public.enroll_report(integer) RETURNS int LANGUAGE sql AS $$SELECT $1+1$$`); err != nil {
		t.Fatal(err)
	}
	if w := post(user, updated, fresh); w.Code != 409 {
		t.Fatalf("function replacement accepted: %d", w.Code)
	}
	remove := url.Values{"mode": {"remove"}, "target": {choice(updated, target)}}
	if w := post(user, updated, remove); w.Code != 303 {
		t.Fatalf("could not remove changed target: %d", w.Code)
	}
	updated, err = app.projects.find(ctx, user.ID, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err = app.vault.open(updated, updated.Secret)
	if err != nil || len(secrets.Enrollments) != 0 {
		t.Fatal("removed enrollment retained")
	}
	// The catalog transport is substituted for Cron only because this local
	// PostgreSQL has no pg_cron extension. Real function identity reads above
	// exercise PostgreSQL; this checks the same endpoint's window storage.
	job := repairEnrollmentTarget{Kind: "cron", DatabaseOID: target.DatabaseOID, OwnerOID: target.OwnerOID, JobID: 42, Name: "billing refresh", Schedule: "*/5 * * * *", CommandDigest: strings.Repeat("a", 64), Fingerprint: strings.Repeat("b", 64), Generation: "9"}
	selectedJobs = []repairEnrollmentTarget{job}
	jobFields := url.Values{"mode": {"enroll"}, "target": {choice(updated, job)}, "proposed_minute": {"2"}, "deadline_seconds": {"200"}, "max_runtime_seconds": {"20"}}
	if w := post(user, updated, jobFields); w.Code != 303 {
		t.Fatalf("Cron enrollment failed: %d %s", w.Code, w.Body.String())
	}
	updated, err = app.projects.find(ctx, user.ID, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err = app.vault.open(updated, updated.Secret)
	if err != nil || len(secrets.Enrollments) != 1 || secrets.Enrollments[0].Window == nil || secrets.Enrollments[0].Window.ProposedMinute != 2 || secrets.Enrollments[0].Window.DeadlineSeconds != 200 {
		t.Fatal("approved Cron window not stored")
	}
	jobFields.Set("target", choice(updated, job))
	selectedJobs[0].Fingerprint = strings.Repeat("c", 64)
	if w := post(user, updated, jobFields); w.Code != 409 {
		t.Fatalf("changed Cron command accepted: %d", w.Code)
	}
	secrets.Capabilities = []string{"memory"}
	sealed, err := app.vault.seal(updated, secrets)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.projects.replaceActivePermissions(ctx, updated, sealed); err != nil {
		t.Fatal(err)
	}
	updated, err = app.projects.find(ctx, user.ID, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	jobFields.Set("target", choice(updated, selectedJobs[0]))
	if w := post(user, updated, jobFields); w.Code != 403 {
		t.Fatalf("unchecked Cron permission accepted: %d", w.Code)
	}
	path := "https://preview.example.com/app/projects/" + row.ID + "/repair-enrollment"
	for _, method := range []string{"GET", "POST"} {
		w := httptest.NewRecorder()
		app.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		if w.Code != 303 || w.Header().Get("Location") != "/signin" {
			t.Fatalf("anonymous enrollment route allowed: %d", w.Code)
		}
	}
	r := httptest.NewRequest("POST", path, strings.NewReader(fields.Encode()))
	r.Header.Set("Origin", "https://attacker.example.com")
	r.AddCookie(&http.Cookie{Name: app.cookie, Value: session})
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin enrollment accepted")
	}
}
