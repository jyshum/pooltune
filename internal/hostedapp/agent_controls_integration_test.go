//go:build integration

package hostedapp

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"pooltune/internal/hostedquery"
	"pooltune/internal/testpg"
)

func TestAgentControlsRetainHistoryAndFenceOwnerActions(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t)
	pc, err := pgxpool.ParseConfig(cfg.ConnString())
	if err != nil {
		t.Fatal(err)
	}
	pc.ConnConfig = cfg
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	const origin = "https://preview.example.com"
	a, err := New(ctx, pool, Config{Origin: origin, InviteCode: "private-local-test-invite", Key: bytes.Repeat([]byte{42}, 32), Sampling: hostedquery.SamplingPolicy{Interval: time.Minute, Window: 5 * time.Minute}, QueueInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	user, owner, err := a.accounts.Register(ctx, "agent-owner@example.com", "my correct test password")
	if err != nil {
		t.Fatal(err)
	}
	_, stranger, err := a.accounts.Register(ctx, "stranger@example.com", "my correct test password")
	if err != nil {
		t.Fatal(err)
	}
	id, err := a.projects.create(ctx, user.ID, "Example", "abcdefghijklmnopqrst")
	if err != nil {
		t.Fatal(err)
	}
	find := func() projectRecord {
		t.Helper()
		row, err := a.projects.find(ctx, user.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	row := find()
	sealed, err := a.vault.seal(row, projectSecrets{Automatic: true, Capabilities: []string{"index"}, RepairsPaused: true, RepairPassword: "unusable-test-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.projects.activate(ctx, row, sealed); err != nil {
		t.Fatal(err)
	}
	row = find()
	project, _ := row.identity()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO pooltune_worker.project_polls(project_id,poll_at,target_ms) VALUES($1,statement_timestamp(),1)`, project.ID())
	post := func(who, from, route, mode string, epoch int64) int {
		r := httptest.NewRequest(http.MethodPost, origin+"/app/projects/"+id+route, strings.NewReader(url.Values{"epoch": {fmt.Sprint(epoch)}, "mode": {mode}}.Encode()))
		r.AddCookie(&http.Cookie{Name: a.cookie, Value: who})
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", from)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w.Code
	}
	for _, route := range []string{"/agent-mode", "/changes/1/revert"} {
		if got := post(stranger, origin, route, "pause", row.Epoch); got != 404 {
			t.Fatalf("cross-account %s returned %d", route, got)
		}
		if got := post(owner, "https://other.example.com", route, "pause", row.Epoch); got != 403 {
			t.Fatalf("cross-origin %s returned %d", route, got)
		}
	}
	if got := post(owner, origin, "/changes/1/revert", "", row.Epoch); got != 409 {
		t.Fatalf("unrecorded revert returned %d", got)
	}
	exec(`INSERT INTO pooltune_worker.managed_cron_witnesses(project_id,name,epoch,payload) VALUES($1,'test',$2,'{}')`, project.ID(), row.Epoch)
	if got := post(owner, origin, "/agent-mode", "pause", row.Epoch); got != 409 {
		t.Fatalf("pause returned %d", got)
	}
	paused := find()
	if paused.Epoch != row.Epoch+1 || !paused.AgentPaused || !bytes.Equal(paused.Secret, row.Secret) {
		t.Fatal("pause lost registration, preference, or credential")
	}
	if got := post(owner, origin, "/agent-mode", "resume", row.Epoch); got != 409 {
		t.Fatalf("stale resume returned %d", got)
	}
	if err := a.projects.admitModel(ctx, id, paused.Epoch); !errors.Is(err, hostedquery.ErrProjectModelBudget) {
		t.Fatalf("paused model admission: %v", err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = hostedquery.ProjectMonitorEpoch(ctx, conn.Conn(), project)
	var signed int64
	if err := conn.QueryRow(ctx, "SELECT monitor_epoch FROM pooltune_worker.projects WHERE project_id=$1", project.ID()).Scan(&signed); err != nil || signed != -paused.Epoch {
		t.Fatalf("old supervisors could start the paused epoch %d: %v", signed, err)
	}
	conn.Release()
	if !errors.Is(err, hostedquery.ErrProjectDisconnected) {
		t.Fatalf("standalone monitor fetched paused epoch: %v", err)
	}
	if got := post(owner, origin, "/agent-mode", "resume", paused.Epoch); got != 409 || !find().AgentPaused {
		t.Fatalf("unresolved cleanup permitted resume: %d", got)
	}
	exec(`UPDATE pooltune_worker.managed_cron_witnesses SET removed_at=statement_timestamp() WHERE project_id=$1`, project.ID())
	if err := a.runProject(ctx, paused); !errors.Is(err, hostedquery.ErrProjectDisconnected) {
		t.Fatalf("paused supervisor started a monitor: %v", err)
	}
	active, err := a.projects.active(ctx)
	if err != nil || len(active) != 0 {
		t.Fatalf("paused registration scheduled for monitoring: %v", err)
	}
	activity, err := hostedquery.ReadProjectActivity(ctx, pool, project.ID(), time.Now().Add(-time.Hour))
	if err != nil || len(activity.Polls) != 1 {
		t.Fatalf("history lost during pause: %v", err)
	}
	if got := post(owner, origin, "/agent-mode", "resume", paused.Epoch); got != 303 {
		t.Fatalf("resume returned %d", got)
	}
	active, err = a.projects.active(ctx)
	if err != nil || len(active) != 1 || active[0].AgentPaused || active[0].Epoch != paused.Epoch+1 || !bytes.Equal(active[0].Secret, sealed) {
		t.Fatalf("resume lost original registration: %v", err)
	}
	if got := post(owner, origin, "/agent-mode", "pause", active[0].Epoch); got != 303 {
		t.Fatalf("pause without a pending scheduler returned %d", got)
	}

	if got := post(owner, origin, "/disconnect", "", row.Epoch); got != 409 {
		t.Fatalf("stale disconnect returned %d", got)
	}
	if err := a.projects.disconnect(ctx, row); !errors.Is(err, hostedquery.ErrProjectDisconnected) {
		t.Fatalf("stale final disconnect cleared current registration: %v", err)
	}
	if !find().Configured {
		t.Fatal("stale confirmation disconnected the installation")
	}

}
