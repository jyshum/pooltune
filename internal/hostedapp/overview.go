package hostedapp

import (
	"fmt"
	"strconv"
	"time"

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

func overviewCharts(activity hostedquery.ProjectActivity, now time.Time) []activityChart {
	// Bucket the trailing 24 hours into consecutive one-hour intervals.
	if !activity.ChartEnd.IsZero() {
		now = activity.ChartEnd
	}
	start := now.UTC().Add(-24 * time.Hour)
	type bucket struct {
		count         [3]int64
		callsMeasured bool
	}
	var hours [24]bucket
	at := func(t time.Time) *bucket {
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
