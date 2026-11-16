package hostedquery

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrManagedTrafficRegression = errors.New("a protected normal-traffic query regressed")

type managedControlCounter struct {
	Query   int64
	Kind    string
	Calls   int64
	TotalMS float64
	Since   time.Time
}
type managedTrafficControls struct {
	ReadMS, WriteMS       float64
	ReadCalls, WriteCalls int64
	means                 map[int64]float64
	counters              map[int64]managedControlCounter
}

// observeManagedControls measures the actual application's existing read/write
// counters. It never generates writes against customer tables.
func observeManagedControls(ctx context.Context, db *pgx.Conn, database, role uint32, candidate int64, baseline *managedTrafficControls) (*managedTrafficControls, error) {
	var schema string
	if err := db.QueryRow(ctx, `SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='pg_stat_statements'`).Scan(&schema); err != nil {
		return nil, err
	}
	read := func() (map[int64]managedControlCounter, error) {
		rows, err := db.Query(ctx, `SELECT queryid,CASE WHEN ltrim(query)~*'^SELECT[[:space:]]' THEN 'read' ELSE 'write' END,calls,total_exec_time,stats_since FROM `+qualified(schema, "pg_stat_statements")+` WHERE dbid=$1 AND userid=$2 AND queryid<>$3 AND toplevel AND calls>0 AND ltrim(query)~*'^(SELECT|INSERT|UPDATE|DELETE)[[:space:]]' ORDER BY calls DESC,queryid LIMIT 32`, database, role, candidate)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		values := make(map[int64]managedControlCounter)
		for rows.Next() {
			var c managedControlCounter
			if err := rows.Scan(&c.Query, &c.Kind, &c.Calls, &c.TotalMS, &c.Since); err != nil {
				return nil, err
			}
			values[c.Query] = c
		}
		return values, rows.Err()
	}
	before, err := read()
	if err != nil {
		return nil, err
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	after, err := read()
	if err != nil {
		return nil, err
	}
	result := &managedTrafficControls{means: make(map[int64]float64), counters: make(map[int64]managedControlCounter)}
	regressed := false
	for query, prior := range before {
		if baseline != nil {
			if _, ok := baseline.means[query]; !ok {
				continue
			}
			saved, ok := baseline.counters[query]
			if !ok || !prior.Since.Equal(saved.Since) || prior.Calls < saved.Calls || prior.TotalMS < saved.TotalMS {
				return nil, errors.New("protected traffic counters changed generation between measurements")
			}
		}
		current, ok := after[query]
		if !ok || !current.Since.Equal(prior.Since) || current.Calls <= prior.Calls || current.TotalMS < prior.TotalMS {
			continue
		}
		calls := current.Calls - prior.Calls
		elapsed := current.TotalMS - prior.TotalMS
		mean := elapsed / float64(calls)
		if baseline != nil && mean > baseline.means[query]*1.25 {
			regressed = true
		}
		result.means[query] = mean
		result.counters[query] = current
		if current.Kind == "read" {
			result.ReadCalls += calls
			result.ReadMS += elapsed
		} else {
			result.WriteCalls += calls
			result.WriteMS += elapsed
		}
	}
	if result.ReadCalls == 0 || result.WriteCalls == 0 {
		return nil, errors.New("protected normal reads and writes were not observed")
	}
	if baseline != nil && len(result.means) != len(baseline.means) {
		return nil, errors.New("a protected query did not have comparable traffic")
	}
	result.ReadMS /= float64(result.ReadCalls)
	result.WriteMS /= float64(result.WriteCalls)
	if regressed {
		return result, ErrManagedTrafficRegression
	}
	return result, nil
}
