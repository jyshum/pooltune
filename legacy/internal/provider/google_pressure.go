package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"
)

// GooglePressureMaxAge includes the documented60s sample cadence, up to165s
// publication lag, and one15s poll. This is delayed host evidence, not an
// instantaneous overload detector. Direct SQL inventory remains a separate gate.
const GooglePressureMaxAge = 240 * time.Second

func (a *Adapter) googlePressure(ctx context.Context) (Pressure, error) {
	now := time.Now().UTC()

	token, e := a.cfg.Token(ctx)
	if e != nil || token == "" {
		return Pressure{}, ErrAPI
	}
	combined := struct {
		TimeSeries []json.RawMessage `json:"timeSeries"`
	}{}
	// Cloud Monitoring accepts only one metric type per time-series request.
	for _, metric := range []string{"database/cpu/utilization", "database/memory/utilization"} {
		filter := `resource.type="cloudsql_database" AND resource.labels.database_id="` + a.cfg.Project + `:` + a.cfg.Instance + `" AND metric.type="cloudsql.googleapis.com/` + metric + `"`
		q := url.Values{"filter": {filter}, "interval.startTime": {now.Add(-5 * time.Minute).Format(time.RFC3339)}, "interval.endTime": {now.Format(time.RFC3339)}, "view": {"FULL"}, "pageSize": {"100"}}
		req, e := http.NewRequestWithContext(ctx, "GET", "https://monitoring.googleapis.com/v3/projects/"+url.PathEscape(a.cfg.Project)+"/timeSeries?"+q.Encode(), nil)
		if e != nil {
			return Pressure{}, ErrAPI
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, e := a.client.Do(req)
		if e != nil {
			return Pressure{}, ErrAPI
		}
		raw, e := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		resp.Body.Close()
		if e != nil || resp.StatusCode != 200 || len(raw) > 1<<20 {
			return Pressure{}, ErrAPI
		}
		var page struct {
			TimeSeries    []json.RawMessage
			NextPageToken string
		}
		if json.Unmarshal(raw, &page) != nil || page.NextPageToken != "" || len(page.TimeSeries) != 1 {
			return Pressure{}, ErrAPI
		}
		combined.TimeSeries = append(combined.TimeSeries, page.TimeSeries...)
	}
	raw, _ := json.Marshal(combined)
	return parseGooglePressure(raw, a.cfg.Project, a.cfg.Instance, time.Now().UTC())
}

func parseGooglePressure(raw []byte, project, instance string, now time.Time) (Pressure, error) {
	var body struct {
		NextPageToken string
		TimeSeries    []struct {
			Metric   struct{ Type string }
			Resource struct {
				Type   string
				Labels map[string]string
			}
			Points []struct {
				Interval struct{ EndTime time.Time }
				Value    struct{ DoubleValue *float64 }
			}
		}
	}
	if json.Unmarshal(raw, &body) != nil || body.NextPageToken != "" || len(body.TimeSeries) != 2 {
		return Pressure{}, ErrAPI
	}
	p := Pressure{Source: "google:" + project + ":" + instance}
	seen := map[string]bool{}
	for _, s := range body.TimeSeries {
		if s.Resource.Type != "cloudsql_database" || s.Resource.Labels["project_id"] != project || s.Resource.Labels["database_id"] != project+":"+instance || seen[s.Metric.Type] {
			return Pressure{}, ErrAPI
		}
		seen[s.Metric.Type] = true
		at := time.Time{}
		var value float64
		for _, v := range s.Points {
			if v.Value.DoubleValue == nil || v.Interval.EndTime.After(now) {
				return Pressure{}, ErrAPI
			}
			if v.Interval.EndTime.After(at) {
				at = v.Interval.EndTime
				value = *v.Value.DoubleValue
			}
		}
		if at.IsZero() {
			return Pressure{}, ErrAPI
		}
		if p.At.IsZero() || at.Before(p.At) {
			p.At = at
		}
		switch s.Metric.Type {
		case "cloudsql.googleapis.com/database/cpu/utilization":
			p.CPU = value
		case "cloudsql.googleapis.com/database/memory/utilization":
			p.Memory = value
		default:
			return Pressure{}, ErrAPI
		}
	}
	if !validPressure(p, now, GooglePressureMaxAge) {
		return Pressure{}, ErrAPI
	}
	return p, nil
}
