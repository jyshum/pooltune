package provider

import (
	"context"
	"encoding/json"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Pressure is database-host evidence. At is the provider's source timestamp,
// never the HTTP receipt time. These delayed samples are not a fast overload stop.
// AzurePressureMaxAge allows a 60s emission interval, up to 60s publication,
// and a 15s polling allowance. Source timestamps are never rewritten.
// This is delayed host evidence, not real-time overload protection.
const AzurePressureMaxAge = 135 * time.Second

type Pressure struct {
	At          time.Time
	CPU, Memory float64
	Source      string
}
type pressureHistory struct {
	at       time.Time
	counters map[string]float64
	cached   *Pressure
}

func validPressure(p Pressure, now time.Time, age time.Duration) bool {
	return !p.At.IsZero() && !p.At.After(now) && now.Sub(p.At) <= age && !math.IsNaN(p.CPU) && !math.IsNaN(p.Memory) && p.CPU >= 0 && p.CPU <= 1 && p.Memory >= 0 && p.Memory <= 1
}
func (a *Adapter) Pressure(ctx context.Context) (Pressure, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now().UTC()
	switch a.cfg.Kind {
	case Google:
		return a.googlePressure(ctx)
	case Azure:
		q := url.Values{"api-version": {"2018-01-01"}, "metricnames": {"cpu_percent,memory_percent"}, "interval": {"PT1M"}, "aggregation": {"Maximum"}, "timespan": {now.Add(-5*time.Minute).Format(time.RFC3339) + "/" + now.Format(time.RFC3339)}}
		raw, _, e := a.request(ctx, "GET", a.azureBase()+"/providers/microsoft.insights/metrics?"+q.Encode(), nil)
		if e != nil {
			return Pressure{}, e
		}
		p, e := parseAzurePressure(raw, now)
		p.Source = "azure:" + a.azureBase()
		return p, e
	case Supabase:
		raw, _, e := a.request(ctx, "GET", a.cfg.Endpoint+"/v1/projects/"+url.PathEscape(a.cfg.Project)+"/analytics/endpoints/metrics", nil)
		if e != nil {
			return Pressure{}, e
		}
		// Scrape timestamps can be generated while the HTTP request is in flight.
		// Validate against receipt time, but retain the provider's source time.
		p, e := a.pressure.supabase(raw, a.cfg.Project, time.Now().UTC())
		p.Source = "supabase:" + a.cfg.Project
		return p, e
	default:
		return Pressure{}, ErrUnsupported
	}
}
func parseAzurePressure(raw []byte, now time.Time) (Pressure, error) {
	var payload struct {
		Value []struct {
			Name       struct{ Value string }
			Timeseries []struct {
				Data []struct {
					TimeStamp time.Time
					Maximum   *float64
				}
			}
		}
	}
	if json.Unmarshal(raw, &payload) != nil {
		return Pressure{}, ErrAPI
	}
	type point struct {
		at    time.Time
		value float64
	}
	found := map[string]point{}
	for _, m := range payload.Value {
		name := m.Name.Value
		if name != "cpu_percent" && name != "memory_percent" {
			continue
		}
		if len(m.Timeseries) != 1 {
			return Pressure{}, ErrUnsupported
		}
		for _, v := range m.Timeseries[0].Data {
			if v.Maximum == nil {
				continue
			}
			if v.TimeStamp.After(now) || *v.Maximum < 0 || *v.Maximum > 100 {
				return Pressure{}, ErrAPI
			}
			if v.TimeStamp.After(found[name].at) {
				found[name] = point{v.TimeStamp, *v.Maximum / 100}
			}
		}
	}
	cpu, cok := found["cpu_percent"]
	mem, mok := found["memory_percent"]
	if !cok || !mok {
		return Pressure{}, ErrAPI
	}
	at := cpu.at
	if mem.at.Before(at) {
		at = mem.at
	}
	p := Pressure{At: at, CPU: cpu.value, Memory: mem.value}
	if !validPressure(p, now, AzurePressureMaxAge) {
		return Pressure{}, ErrAPI
	}
	return p, nil
}

var metricLine = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)\{([^}]*)\} ([^ ]+)(?: [0-9]+)?$`)
var labelPair = regexp.MustCompile(`(?:^|,)([a-zA-Z_][a-zA-Z0-9_]*)="((?:[^"\\]|\\.)*)"`)

func metricLabels(s string) (map[string]string, error) {
	out := map[string]string{}
	end := 0
	for _, pos := range labelPair.FindAllStringSubmatchIndex(s, -1) {
		if pos[0] != end {
			return nil, ErrAPI
		}
		key := s[pos[2]:pos[3]]
		if _, ok := out[key]; ok {
			return nil, ErrAPI
		}
		value, e := strconv.Unquote(`"` + s[pos[4]:pos[5]] + `"`)
		if e != nil {
			return nil, ErrAPI
		}
		out[key] = value
		end = pos[1]
	}
	if end != len(s) {
		return nil, ErrAPI
	}
	return out, nil
}
func (h *pressureHistory) supabase(raw []byte, project string, now time.Time) (Pressure, error) {
	values := map[string]float64{}
	counters := map[string]float64{}
	cpus := map[string]int{}
	modes := map[string]bool{"idle": true, "iowait": true, "irq": true, "nice": true, "softirq": true, "steal": true, "system": true, "user": true}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "node_cpu_seconds_total{") && !strings.HasPrefix(line, "node_time_seconds{") && !strings.HasPrefix(line, "node_memory_MemTotal_bytes{") && !strings.HasPrefix(line, "node_memory_MemAvailable_bytes{") {
			continue
		}
		m := metricLine.FindStringSubmatch(line)
		if len(m) != 4 {
			return Pressure{}, ErrAPI
		}
		labels, e := metricLabels(m[2])
		if e != nil {
			return Pressure{}, e
		}
		if labels["service_type"] != "db" {
			continue
		}
		if labels["supabase_project_ref"] != project || labels["supabase_identifier"] != project {
			return Pressure{}, ErrUnsupported
		}
		n, e := strconv.ParseFloat(m[3], 64)
		if e != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
			return Pressure{}, ErrAPI
		}
		if m[1] == "node_cpu_seconds_total" {
			mode := labels["mode"]
			if !modes[mode] || labels["cpu"] == "" {
				return Pressure{}, ErrAPI
			}
			key := labels["cpu"] + "/" + mode
			if _, ok := counters[key]; ok {
				return Pressure{}, ErrAPI
			}
			counters[key] = n
			cpus[labels["cpu"]]++
		} else {
			if _, ok := values[m[1]]; ok {
				return Pressure{}, ErrUnsupported
			}
			values[m[1]] = n
		}
	}
	if len(values) != 3 || len(cpus) == 0 {
		return Pressure{}, ErrAPI
	}
	for _, n := range cpus {
		if n != 8 {
			return Pressure{}, ErrAPI
		}
	}
	stamp := values["node_time_seconds"]
	at := time.Unix(int64(stamp), int64((stamp-math.Floor(stamp))*1e9))
	total, available := values["node_memory_MemTotal_bytes"], values["node_memory_MemAvailable_bytes"]
	if total <= 0 || available > total || at.After(now) || now.Sub(at) > 75*time.Second {
		return Pressure{}, ErrAPI
	}
	p := Pressure{At: at, Memory: 1 - available/total}
	if !h.at.IsZero() && at.Equal(h.at) {
		if h.cached == nil {
			return Pressure{}, ErrPending
		}
		for k, v := range counters {
			if h.counters[k] != v {
				return Pressure{}, ErrAPI
			}
		}
		if !validPressure(*h.cached, now, 75*time.Second) {
			return Pressure{}, ErrAPI
		}
		return *h.cached, nil
	}
	previous, oldAt := h.counters, h.at
	h.at = at
	h.counters = counters
	h.cached = nil
	if previous == nil || !at.After(oldAt) || at.Sub(oldAt) > 120*time.Second || len(previous) != len(counters) {
		return Pressure{}, ErrPending
	}
	sum, idle := 0.0, 0.0
	for k, v := range counters {
		old, ok := previous[k]
		if !ok || v < old {
			return Pressure{}, ErrAPI
		}
		delta := v - old
		sum += delta
		if strings.HasSuffix(k, "/idle") {
			idle += delta
		}
	}
	if sum <= 0 || idle > sum {
		return Pressure{}, ErrAPI
	}
	p.CPU = 1 - idle/sum
	if !validPressure(p, now, 75*time.Second) {
		return Pressure{}, ErrAPI
	}
	h.cached = &p
	return p, nil
}
