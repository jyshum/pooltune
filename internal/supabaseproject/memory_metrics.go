package supabaseproject

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var ErrMemoryHeadroomUnavailable = errors.New("fresh project database memory metrics are unavailable")

type MemoryHeadroom struct {
	ProjectRef     string    `json:"project_ref"`
	ObservedAt     time.Time `json:"observed_at"`
	AvailableBytes int64     `json:"available_bytes"`
	TotalBytes     int64     `json:"total_bytes"`
}

type MemoryMetricsReader struct {
	client             *http.Client
	endpoint, key, ref string
	resolveToken       func(context.Context) (string, error)
}

func NewMemoryMetricsReader(ref, secret string) (*MemoryMetricsReader, error) {
	if !projectRef.MatchString(ref) || !strings.HasPrefix(secret, "sbp_fc") || strings.ContainsAny(secret, " \r\n\t") {
		return nil, ErrMemoryHeadroomUnavailable
	}
	return &MemoryMetricsReader{client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, endpoint: "https://api.supabase.com/v1/projects/" + ref + "/analytics/endpoints/metrics", key: secret, ref: ref}, nil
}

// NewOAuthMemoryMetricsReader resolves current project-bound Management access.
func NewOAuthMemoryMetricsReader(ref string, resolve func(context.Context) (string, error)) *MemoryMetricsReader {
	return &MemoryMetricsReader{client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, endpoint: "https://api.supabase.com/v1/projects/" + ref + "/analytics/endpoints/metrics", ref: ref, resolveToken: resolve}
}

// Read uses the project's documented Metrics API, not database configuration as
// a proxy for free memory. Credentials and response bodies never enter errors.
func (r *MemoryMetricsReader) Read(ctx context.Context, ref string) (MemoryHeadroom, error) {
	if r == nil || r.ref != ref || !projectRef.MatchString(ref) {
		return MemoryHeadroom{}, ErrMemoryHeadroomUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.endpoint, nil)
	if err != nil {
		return MemoryHeadroom{}, ErrMemoryHeadroomUnavailable
	}
	key := r.key
	if r.resolveToken != nil {
		key, err = r.resolveToken(ctx)
		if err != nil || len(key) == 0 || len(key) > 16384 || strings.ContainsAny(key, " \t\r\n") {
			return MemoryHeadroom{}, ErrMemoryHeadroomUnavailable
		}
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := r.client.Do(req)
	if err != nil {
		return MemoryHeadroom{}, ErrMemoryHeadroomUnavailable
	}
	defer resp.Body.Close()
	now := time.Now().UTC()
	date, err := http.ParseTime(resp.Header.Get("Date"))
	if err != nil || date.After(now.Add(5*time.Second)) || now.Sub(date) > 15*time.Second || resp.StatusCode != http.StatusOK || resp.Header.Get("Age") != "" && resp.Header.Get("Age") != "0" {
		return MemoryHeadroom{}, ErrMemoryHeadroomUnavailable
	}
	const limit = 2 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(body) > limit {
		return MemoryHeadroom{}, ErrMemoryHeadroomUnavailable
	}
	return parseMemoryHeadroom(string(body), ref, now)
}

var memoryMetricLine = regexp.MustCompile(`^(node_memory_MemAvailable_bytes|node_memory_MemTotal_bytes)\{([^{}]*)\}\s+([^\s]+)(?:\s+([0-9]+))?$`)
var memoryMetricLabel = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)="([^"\\]*)"(?:,|$)`)

func parseMemoryHeadroom(body, ref string, at time.Time) (MemoryHeadroom, error) {
	out := MemoryHeadroom{ProjectRef: ref, ObservedAt: at}
	seen := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "node_memory_MemAvailable_bytes") && !strings.HasPrefix(line, "node_memory_MemTotal_bytes") {
			continue
		}
		match := memoryMetricLine.FindStringSubmatch(line)
		if match == nil {
			return MemoryHeadroom{}, ErrMemoryHeadroomUnavailable
		}
		labels := map[string]string{}
		raw := match[2]
		for len(raw) > 0 {
			loc := memoryMetricLabel.FindStringSubmatchIndex(raw)
			if loc == nil || loc[0] != 0 {
				return MemoryHeadroom{}, ErrMemoryHeadroomUnavailable
			}
			key, val := raw[loc[2]:loc[3]], raw[loc[4]:loc[5]]
			if _, exists := labels[key]; exists {
				return MemoryHeadroom{}, ErrMemoryHeadroomUnavailable
			}
			labels[key] = val
			raw = raw[loc[1]:]
		}
		if labels["supabase_project_ref"] != ref || labels["service_type"] != "db" {
			continue
		}
		if seen[match[1]] {
			return MemoryHeadroom{}, ErrMemoryHeadroomUnavailable
		}
		seen[match[1]] = true
		value, err := strconv.ParseFloat(match[3], 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > float64(1<<53) || math.Trunc(value) != value {
			return MemoryHeadroom{}, ErrMemoryHeadroomUnavailable
		}
		if match[4] != "" {
			ms, err := strconv.ParseInt(match[4], 10, 64)
			if err != nil || time.UnixMilli(ms).After(at.Add(5*time.Second)) || at.Sub(time.UnixMilli(ms)) > 15*time.Second {
				return MemoryHeadroom{}, ErrMemoryHeadroomUnavailable
			}
		}
		if match[1] == "node_memory_MemAvailable_bytes" {
			out.AvailableBytes = int64(value)
		} else {
			out.TotalBytes = int64(value)
		}
	}
	if !seen["node_memory_MemAvailable_bytes"] || !seen["node_memory_MemTotal_bytes"] || out.TotalBytes <= 0 || out.AvailableBytes > out.TotalBytes {
		return MemoryHeadroom{}, ErrMemoryHeadroomUnavailable
	}
	return out, nil
}

// MemoryAllowanceSlots preserves substantial unallocated headroom. It limits
// added sort memory to half of memory above max(128MiB,25% of total), and never
// admits more than three simultaneous 32MiB allowances in this database.
func (h MemoryHeadroom) MemoryAllowanceSlots(now time.Time) int {
	if !projectRef.MatchString(h.ProjectRef) || h.ObservedAt.After(now) || now.Sub(h.ObservedAt) > 15*time.Second || h.TotalBytes <= 0 || h.AvailableBytes > h.TotalBytes {
		return 0
	}
	reserve := max(int64(128<<20), h.TotalBytes/4)
	available := h.AvailableBytes - reserve
	if available <= 0 {
		return 0
	}
	return min(3, int(available/(2*(32<<20))))
}
