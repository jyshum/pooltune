package hostedquery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const trafficPlanAge = 2 * time.Minute
const trafficPlanNodes = "'Seq Scan','Index Scan','Index Only Scan','Bitmap Heap Scan','Bitmap Index Scan','Limit','Sort','Incremental Sort','Aggregate','Group','Unique','Gather','Gather Merge','Hash Join','Merge Join','Nested Loop','Hash','Materialize','Append','Result'"

var directLoginName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z_0-9]{0,62}$`)

// TrafficPlan summarizes one execution from Supabase's ordinary query logs.
// Counts describe the root. The first child, if present, describes shape only.
type TrafficPlan struct {
	CapturedAt    time.Time `json:"captured_at"`
	Node          string    `json:"node"`
	Child         string    `json:"child_node,omitempty"`
	EstimatedRows int64     `json:"estimated_rows"`
	ActualRows    int64     `json:"actual_rows"`
	FilteredRows  *int64    `json:"filtered_rows,omitempty"`
	SharedHits    int64     `json:"shared_hit_blocks"`
	SharedReads   int64     `json:"shared_read_blocks"`
	ExecutionMS   float64   `json:"execution_ms"`
}

type TrafficPlanReader struct {
	client       *http.Client
	endpoint     string
	token        string
	ref          string
	resolveToken func(context.Context) (string, error)
}

// NewTrafficPlanReader configures optional read-only log access. A fine-grained
// token must be provisioned for just this project with Analytics Logs Read.
func NewTrafficPlanReader(token string) (*TrafficPlanReader, error) {
	if token == "" {
		return nil, nil
	}
	if !strings.HasPrefix(token, "sbp_fc") || strings.ContainsAny(token, " \r\n\t") {
		return nil, errors.New("traffic plans require a project-scoped Supabase logs token")
	}
	client := &http.Client{Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &TrafficPlanReader{client: client, endpoint: "https://api.supabase.com", token: token}, nil
}

// NewOAuthTrafficPlanReader binds dynamic Management OAuth access to one project.
func NewOAuthTrafficPlanReader(ref string, resolve func(context.Context) (string, error)) *TrafficPlanReader {
	return &TrafficPlanReader{client: &http.Client{Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, endpoint: "https://api.supabase.com", ref: ref, resolveToken: resolve}
}

type trafficLogRow struct {
	Captured  int64  `json:"captured_at_ms"`
	Node      string `json:"node"`
	Child     string `json:"child"`
	Estimated string `json:"estimated_rows"`
	Actual    string `json:"actual_rows"`
	Loops     string `json:"loops"`
	Filtered  string `json:"filtered_rows"`
	Hits      string `json:"shared_hit_blocks"`
	Reads     string `json:"shared_read_blocks"`
	Duration  string `json:"execution_ms"`
}

func (r *TrafficPlanReader) read(ctx context.Context, ref, login string, query int64, start, end time.Time) (*TrafficPlan, string) {
	if !directLoginName.MatchString(login) || query == 0 || !start.Before(end) || end.Sub(start) > trafficPlanAge {
		return nil, "invalid_request"
	}
	args := url.Values{
		"sql":                 {trafficPlanQuery(ref, login, query, start, end)},
		"iso_timestamp_start": {start.UTC().Format(time.RFC3339Nano)},
		"iso_timestamp_end":   {end.UTC().Format(time.RFC3339Nano)},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.endpoint+"/v1/projects/"+ref+"/analytics/endpoints/logs?"+args.Encode(), nil)
	if err != nil {
		return nil, "invalid_request"
	}
	token := r.token
	if r.resolveToken != nil {
		if ref != r.ref {
			return nil, "invalid_request"
		}
		token, err = r.resolveToken(ctx)
		if err != nil || len(token) == 0 || len(token) > 16384 || strings.ContainsAny(token, " \t\r\n") {
			return nil, "authorization_unavailable"
		}
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := r.client.Do(request)
	if err != nil {
		return nil, "transport_error"
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Sprintf("http_%d", response.StatusCode)
	}
	const responseLimit = 8192
	body, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil || len(body) > responseLimit || !json.Valid(body) {
		return nil, "invalid_response"
	}
	var data struct {
		Result []trafficLogRow `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&data) != nil || len(data.Result) > 4 {
		return nil, "invalid_response"
	}
	if len(data.Error) > 0 && string(data.Error) != "null" {
		return nil, "provider_error"
	}
	for _, row := range data.Result {
		if sample := row.sample(start, end); sample != nil {
			return sample, "available"
		}
	}
	if len(data.Result) == 0 {
		return nil, "no_plans"
	}
	return nil, "invalid_plan"
}

func (row trafficLogRow) sample(start, end time.Time) *TrafficPlan {
	captured := time.UnixMilli(row.Captured).UTC()
	if captured.Before(start) || captured.After(end) || row.Loops != "1" || !trafficPlanNode(row.Node) || row.Child != "" && !trafficPlanNode(row.Child) {
		return nil
	}
	p := TrafficPlan{CapturedAt: captured, Node: row.Node, Child: row.Child}
	counts := []struct {
		raw  string
		dest *int64
	}{
		{row.Estimated, &p.EstimatedRows}, {row.Actual, &p.ActualRows}, {row.Hits, &p.SharedHits}, {row.Reads, &p.SharedReads},
	}
	var filtered int64
	if row.Filtered != "" {
		counts = append(counts, struct {
			raw  string
			dest *int64
		}{row.Filtered, &filtered})
		p.FilteredRows = &filtered
	}
	for _, count := range counts {
		n, err := strconv.ParseInt(count.raw, 10, 64)
		if err != nil || n < 0 || n > 9007199254740991 {
			return nil
		}
		*count.dest = n
	}
	duration, err := strconv.ParseFloat(row.Duration, 64)
	if err != nil || !nonnegativeFinite(duration) || duration > 60000 {
		return nil
	}
	p.ExecutionMS = duration
	return &p
}

func trafficPlanNode(node string) bool {
	return slices.Contains(strings.Split(strings.Trim(trafficPlanNodes, "'"), "','"), node)
}

// Supabase exposes CSV log metadata as strings. PostgreSQL query IDs are signed
// in pg_stat_statements and may be represented as unsigned in provider logs.
// Project the measurements inside Supabase so raw log messages never arrive.
func trafficPlanQuery(ref, login string, query int64, start, end time.Time) string {
	return fmt.Sprintf(`SELECT toUnixTimestamp64Milli(timestamp) AS captured_at_ms,
   if(root_node IN (%[1]s), root_node, 'Unsupported') AS node,
   if(child_node IN (%[1]s) OR child_node = '', child_node, 'Unsupported') AS child,
   extract(JSONExtractRaw(plan,'Plan Rows'), '^([0-9]{1,16})$') AS estimated_rows,
   extract(JSONExtractRaw(plan,'Actual Rows'), '^([0-9]{1,16})$') AS actual_rows,
   extract(JSONExtractRaw(plan,'Actual Loops'), '^([0-9]{1,16})$') AS loops,
   extract(JSONExtractRaw(plan,'Rows Removed by Filter'), '^([0-9]{1,16})$') AS filtered_rows,
   extract(JSONExtractRaw(plan,'Shared Hit Blocks'), '^([0-9]{1,16})$') AS shared_hit_blocks,
   extract(JSONExtractRaw(plan,'Shared Read Blocks'), '^([0-9]{1,16})$') AS shared_read_blocks,
   execution_ms
 FROM (SELECT timestamp, plan, execution_ms, JSONExtractString(plan, 'Node Type') AS root_node,
        JSONExtractString(plan, 'Plans', 1, 'Node Type') AS child_node
   FROM (SELECT timestamp,
      JSONExtractRaw(extract(event_message, '(?s)^duration: [0-9.]+ ms  plan:\n(\{.*\})$'), 'Plan') AS plan,
      extract(event_message, '^duration: ([0-9]{1,8}[.][0-9]{3}) ms  plan:') AS execution_ms
     FROM logs WHERE source = 'postgres_logs'
       AND log_attributes['host'] = 'db-%[7]s'
       AND log_attributes['identifier'] = '%[7]s'
       AND log_attributes['parsed.database_name'] = 'postgres'
       AND log_attributes['parsed.user_name'] = '%[2]s'
       AND log_attributes['parsed.query_id'] IN ('%[3]d','%[4]d')
       AND log_attributes['parsed.backend_type'] = 'client backend'
       AND log_attributes['parsed.command_tag'] = 'SELECT'
       AND log_attributes['parsed.error_severity'] = 'LOG'
       AND log_attributes['parsed.sql_state_code'] = '00000'
       AND log_attributes['parsed.context'] = ''
       AND toUnixTimestamp64Milli(timestamp) BETWEEN %[5]d AND %[6]d
       AND length(event_message) <= 65536 AND startsWith(event_message, 'duration: ')
       AND position(event_message, '  plan:') > 0
     ORDER BY timestamp DESC LIMIT 4)) ORDER BY timestamp DESC`, trafficPlanNodes, login, query, uint64(query), start.UnixMilli(), end.UnixMilli(), ref)
}
