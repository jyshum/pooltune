package hostedapp

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"time"

	"pooltune/internal/hostedquery"
)

func TestProjectActivityRendersRecordedRepairAndProposalSeparately(t *testing.T) {
	page := Page{Project: ProjectView{ID: "test", Status: "Monitoring"}}
	populateActivity(&page.Project, hostedquery.ProjectActivity{Jobs: []hostedquery.ProjectJob{
		{ID: 1, Outcome: "improved_tested_inputs", Repair: &hostedquery.ProjectRepair{
			Kind: "index", Action: "recorded", Metrics: []hostedquery.ProjectRepairMetric{
				{Label: "Query mean latency", Unit: "ms", Before: 12, After: 0},
			},
		}},
		{ID: 2, Outcome: "propose", Reason: "Try a nonunique index"},
	}}, time.Minute, time.Now())
	templates, err := template.ParseFS(frontend, "ui/*.html")
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	if err := templates.ExecuteTemplate(&body, "project", page); err != nil {
		t.Fatal(err)
	}
	rendered := body.String()
	for _, expected := range []string{
		"Improved on tested inputs", "Action recorded", "Recommendation saved", "Try a nonunique index",
		`aria-label="Before and after measurements for run 1"`,
		`<th scope="row">Query mean latency</th><td>12.00 ms</td><td>0.00 ms</td>`,
	} {
		if !strings.Contains(rendered, expected) {
			t.Errorf("activity page missing %q", expected)
		}
	}
	if strings.Count(rendered, "Action recorded") != 1 || strings.Contains(rendered, "Before and after measurements for run 2") {
		t.Fatal("a proposal-only run rendered a recorded repair")
	}
}
