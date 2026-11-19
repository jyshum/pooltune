package hostedapp

import (
	"testing"
	"time"

	"pooltune/internal/hostedquery"
)

func TestOverviewCountsMeasuredCallsWithinDisplayedHours(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 15, 0, 0, time.UTC)
	old := now.Add(-24*time.Hour - time.Minute)
	zero, calls := int64(0), int64(17)
	activity := hostedquery.ProjectActivity{
		Windows: []hostedquery.ProjectWindow{{End: old, Calls: &calls}, {End: now}, {End: now.Add(-time.Hour), Calls: &zero}, {End: now, Calls: &calls}},
		Polls:   []hostedquery.ProjectPoll{{At: old}, {At: now}},
	}
	charts := overviewCharts(activity, now)
	if charts[0].Total != "1" || charts[1].Total != "17" || charts[1].Bars[23].Height != 60 || charts[2].Total != "0" {
		t.Fatal("chart did not reflect the recorded hours")
	}
	activity.Windows = activity.Windows[:2]
	if overviewCharts(activity, now)[1].Total != "—" {
		t.Fatal("missing calls appeared as a measured zero")
	}
}

func TestQueryGroupsKeepMissingCountersAndSeparateRoles(t *testing.T) {
	zero, three := int64(0), int64(3)
	windows := []hostedquery.ProjectWindow{
		{Database: 1, Role: 2, Query: 7, Calls: &three}, {Database: 1, Role: 2, Query: 7, Calls: &zero},
		{Database: 1, Role: 2, Query: 7}, {Database: 1, Role: 3, Query: 7},
	}
	groups := groupQueryWindows(windows)
	if len(groups) != 2 || groups[0].Measured != 0 || groups[1].Calls != 3 || groups[1].Measured != 2 || groups[1].Windows != 3 {
		t.Fatalf("wrong summaries: %+v", groups)
	}
	if got := groups[1].Recent; got[0].Calls != "—" || got[1].Calls != "0" {
		t.Fatal("missing measurement shown as zero")
	}
}
