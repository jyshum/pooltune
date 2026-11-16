package hostedapp

import (
	"bytes"
	"html/template"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestEnrollmentChoiceBindsOwnerProjectEpochAndExpiry(t *testing.T) {
	v, err := newVault(bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	row := projectRecord{ID: "one", Ref: "abcdefghijklmnopqrst", Owner: 1, Epoch: 3, Configured: true}
	target := repairEnrollmentTarget{Kind: "memory", DatabaseOID: 1, ObjectOID: 2, OwnerOID: 3, Schema: "app", Name: "report", Signature: "app.report(integer)", Fingerprint: "fingerprint", Generation: "7"}
	now := time.Now()
	token, err := v.enrollmentChoice(row, target, now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.openEnrollmentChoice(row, token, now)
	if err != nil || got != target {
		t.Fatalf("selection lost its identity: %+v %v", got, err)
	}
	for _, changed := range []projectRecord{{ID: "one", Ref: row.Ref, Owner: 2, Epoch: 3}, {ID: "two", Ref: row.Ref, Owner: 1, Epoch: 3}, {ID: "one", Ref: row.Ref, Owner: 1, Epoch: 4}} {
		if _, err := v.openEnrollmentChoice(changed, token, now); err == nil {
			t.Fatal("foreign/stale selection accepted")
		}
	}
	if _, err := v.openEnrollmentChoice(row, token, now.Add(16*time.Minute)); err == nil {
		t.Fatal("expired choice accepted")
	}
	if _, err := v.openEnrollmentChoice(row, token+"x", now); err == nil {
		t.Fatal("modified choice accepted")
	}
}

func TestEnrollmentPageExplainsWaitingAndEscapesCatalogNames(t *testing.T) {
	templates, err := template.ParseFS(frontend, "ui/*.html")
	if err != nil {
		t.Fatal(err)
	}
	page := Page{Project: ProjectView{ID: "one", Name: "Example"}, Enrollment: EnrollmentView{MemorySelected: true, CronSelected: true,
		Functions: []EnrollmentOptionView{{Label: `public."<script>"(integer)`, Token: "opaque-choice", Selectable: true}},
		Jobs:      []EnrollmentOptionView{{Label: "Nightly refresh", Schedule: "*/5 * * * *", Token: "opaque-job", Selectable: true, EveryMinutes: 5, DeadlineSeconds: 300}},
	}}
	var output bytes.Buffer
	if err := templates.ExecuteTemplate(&output, "repair-enrollment", page); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Choosing a target allows the background agent", "Application-wide request and error rates remain unobserved", "Check project activity for repair progress and outcomes", "Enroll function", "Save job and allowed schedule"} {
		if !strings.Contains(output.String(), text) {
			t.Fatalf("missing clear enrollment scope %q", text)
		}
	}
	if strings.Contains(output.String(), "<script>") || strings.Contains(output.String(), `name="query_id"`) {
		t.Fatal("catalog markup executed or query ID requested")
	}
}

func TestCronEnrollmentPreservesFrequencyAndRequiresAFeasibleDeadline(t *testing.T) {
	fields := url.Values{"proposed_minute": {"2"}, "deadline_seconds": {"200"}, "max_runtime_seconds": {"20"}}
	w, err := enrollmentCronWindow("*/5 * * * *", fields)
	if err != nil || w.EveryMinutes != 5 || w.CurrentMinute != 0 || w.ProposedMinute != 2 {
		t.Fatalf("valid window: %+v %v", w, err)
	}
	for _, schedule := range []string{"* * * * *", "@hourly", "30 2 * * *", "*/7 * * * *", "*/5 * * * *; SELECT 1"} {
		if _, err := enrollmentCronWindow(schedule, fields); err == nil {
			t.Fatalf("unsupported schedule accepted: %q", schedule)
		}
	}
	for _, bad := range []url.Values{
		{"proposed_minute": {"0"}, "deadline_seconds": {"200"}, "max_runtime_seconds": {"20"}},
		{"proposed_minute": {"2"}, "deadline_seconds": {"130"}, "max_runtime_seconds": {"20"}},
		{"proposed_minute": {"2"}, "deadline_seconds": {"301"}, "max_runtime_seconds": {"20"}},
		{"proposed_minute": {"2"}, "deadline_seconds": {"200"}, "max_runtime_seconds": {"21"}},
	} {
		if _, err := enrollmentCronWindow("*/5 * * * *", bad); err == nil {
			t.Fatalf("unsafe window accepted: %v", bad)
		}
	}
}
