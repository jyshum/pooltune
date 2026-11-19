package hostedapp

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"pooltune/internal/hostedapp/auth"
	"pooltune/internal/hostedquery"
)

type activityChart struct {
	Label, Total, Note string
	Bars               []chartBar
}

type chartBar struct {
	X, Y, Height int
	Label        string
}

type activityBucket struct {
	count         [3]int64
	callsMeasured bool
}

func overviewCharts(activity hostedquery.ProjectActivity, now time.Time) []activityChart {
	// Bucket the trailing 24 hours into consecutive one-hour intervals.
	if !activity.ChartEnd.IsZero() {
		now = activity.ChartEnd
	}
	start := now.UTC().Add(-24 * time.Hour)
	var hours [24]activityBucket
	at := func(t time.Time) *activityBucket {
		if t.Before(start) || t.After(now) {
			return nil
		}
		return &hours[min(23, int(t.Sub(start)/time.Hour))]
	}
	for _, poll := range activity.Polls {
		if b := at(poll.At); b != nil {
			b.count[0]++
		}
	}
	for _, sample := range activity.Windows {
		if b := at(sample.End); b != nil && sample.Calls != nil {
			b.callsMeasured = true
			b.count[1] += *sample.Calls
		}
	}
	for i, count := range activity.HourlyJobs {
		hours[i].count[2] = count
	}
	return chartsFromHours(hours, start)
}

func chartsFromHours(hours [24]activityBucket, start time.Time) []activityChart {
	charts := []activityChart{{Label: "Monitor checks"}, {Label: "Measured query calls"}, {Label: "Recorded investigations"}}
	for metric := range charts {
		var peak, total int64
		var measured bool
		for _, b := range hours {
			total += b.count[metric]
			peak = max(peak, b.count[metric])
			measured = measured || b.callsMeasured
		}
		chart := &charts[metric]
		chart.Total = strconv.FormatInt(total, 10)
		if metric == 1 && !measured {
			chart.Total, chart.Note = "—", "Waiting for measured query windows"
		}
		for i, b := range hours {
			value := b.count[metric]
			height := 0
			if value > 0 {
				height = max(2, int(60*float64(value)/float64(peak)))
			}
			label := fmt.Sprintf("%s · %d", start.Add(time.Duration(i)*time.Hour).Format("15:04 UTC"), value)
			if metric == 1 && !b.callsMeasured {
				label = start.Add(time.Duration(i)*time.Hour).Format("15:04 UTC") + " · No measurement"
			}
			chart.Bars = append(chart.Bars, chartBar{X: i * 10, Y: 64 - height, Height: height, Label: label})
		}
	}
	return charts
}

func cadenceLabel(d time.Duration) string {
	if d == time.Minute {
		return "Scheduled once a minute"
	}
	return "Scheduled every " + d.String()
}

// Refresh only hourly totals. Navigation history and individual query windows
// are deliberately outside the polling path.
func (a *App) projectMetrics(w http.ResponseWriter, r *http.Request, user auth.User) {
	row, ok := a.ownedProject(w, r, user)
	if !ok {
		return
	}
	identity, err := row.identity()
	if err != nil {
		http.Error(w, "Project unavailable", 503)
		return
	}
	now := time.Now().UTC()
	start := now.Add(-24 * time.Hour)
	rows, err := a.projects.db.Query(r.Context(), `
 WITH hourly AS (
   SELECT least(23,floor(extract(epoch FROM poll_at-$2::timestamptz)/3600)::int) AS hour,
          count(*) AS polls, 0::bigint AS calls, false AS measured, 0::bigint AS jobs, max(poll_at) AS last_poll
   FROM pooltune_worker.project_polls WHERE project_id=$1 AND poll_at BETWEEN $2 AND $3 GROUP BY 1
   UNION ALL
   SELECT least(23,floor(extract(epoch FROM window_end-$2::timestamptz)/3600)::int),0,
          coalesce(sum(calls),0),count(calls)>0,0,NULL
   FROM pooltune_worker.project_windows WHERE project_id=$1 AND window_end BETWEEN $2 AND $3 GROUP BY 1
   UNION ALL
   SELECT least(23,floor(extract(epoch FROM created_at-$2::timestamptz)/3600)::int),0,0,false,count(*),NULL
   FROM pooltune_worker.investigations WHERE project_id=$1 AND created_at BETWEEN $2 AND $3 GROUP BY 1
 )
 SELECT hour,sum(polls),sum(calls),bool_or(measured),sum(jobs),max(last_poll) FROM hourly GROUP BY hour
 `, identity.ID(), start, now)
	if err != nil {
		http.Error(w, "Activity unavailable", 503)
		return
	}
	defer rows.Close()
	var hours [24]activityBucket
	var last time.Time
	for rows.Next() {
		var hour int
		var b activityBucket
		var poll *time.Time
		if err := rows.Scan(&hour, &b.count[0], &b.count[1], &b.callsMeasured, &b.count[2], &poll); err != nil {
			http.Error(w, "Activity unavailable", 503)
			return
		}
		hours[hour] = b
		if poll != nil && poll.After(last) {
			last = *poll
		}
	}
	if rows.Err() != nil {
		http.Error(w, "Activity unavailable", 503)
		return
	}
	view := ProjectView{ID: row.ID, Charts: chartsFromHours(hours, start), PollCadence: cadenceLabel(a.policy.Interval)}
	if !last.IsZero() {
		view.LastPoll = displayTime(last)
		if baseProjectView(row).Status == "Monitoring" && now.Sub(last) > 2*a.policy.Interval {
			view.LastPoll += " (delayed)"
		}
	}
	a.render(w, 200, "metrics-response", Page{Project: view})
}
