package hostedquery

import (
	"context"
	"errors"
	"time"

	"pooltune/internal/supabaseproject"

	"github.com/jackc/pgx/v5"
)

var ErrProjectDisconnected = errors.New("Supabase monitor is disconnected")

// ProjectMonitor has one dedicated read-only Supabase session and one state
// session. The operator's enrollment is read from Supabase every poll.
type ProjectMonitor struct {
	target, state *pgx.Conn
	project       supabaseproject.Project
	policy        SamplingPolicy
	epoch         int64
}

func NewProjectMonitor(target, state *pgx.Conn, project supabaseproject.Project, policy SamplingPolicy) (*ProjectMonitor, error) {
	if err := policy.validate(); err != nil {
		return nil, err
	}
	return &ProjectMonitor{target: target, state: state, project: project, policy: policy}, nil
}

func (w *ProjectMonitor) read(ctx context.Context) (Enrollment, map[queryKey]Counter, error) {
	enrollment := Enrollment{Project: w.project.ID(), Queries: []QueryObjective{}}
	rows, err := w.target.Query(ctx, "SELECT * FROM "+qualified(w.project.Role(), "snapshot")+"()")
	if err != nil {
		return Enrollment{}, nil, err
	}
	defer rows.Close()
	samples := make(map[queryKey]Counter)
	for rows.Next() {
		var objective QueryObjective
		var sample Counter
		var entry *time.Time
		var calls, blocks *int64
		var execution *float64
		if err := rows.Scan(&objective.Database, &objective.Role, &objective.Query, &objective.MeanMS,
			&sample.Boot, &sample.Reset, &entry, &calls, &execution, &blocks); err != nil {
			return Enrollment{}, nil, err
		}
		enrollment.Queries = append(enrollment.Queries, objective)
		if len(enrollment.Queries) > maxWorkerQueries {
			return Enrollment{}, nil, errors.New("Supabase monitor returned too many enrolled queries")
		}
		if entry == nil || calls == nil || execution == nil || blocks == nil {
			continue
		}
		sample.Database, sample.Role, sample.Query = objective.Database, objective.Role, objective.Query
		sample.Entry, sample.Calls, sample.ExecutionMS, sample.Blocks = *entry, *calls, *execution, *blocks
		samples[sample.key()] = sample
	}
	if err := rows.Err(); err != nil {
		return Enrollment{}, nil, err
	}
	if len(enrollment.Queries) != 0 {
		if err := enrollment.validate(w.policy); err != nil {
			return Enrollment{}, nil, err
		}
	}
	return enrollment, samples, nil
}

// Connect verifies the owner-installed reader before registering or reactivating
// the project in Pooltune. Credentials never go to the state database.
func (w *ProjectMonitor) Connect(ctx context.Context) (Enrollment, error) {
	return w.connect(ctx, true)
}

// RegisterHosted records the first deployment enrollment. Only explicit Connect
// may reactivate a disconnected installation.
func (w *ProjectMonitor) RegisterHosted(ctx context.Context) (int64, error) {
	epoch, err := HostedProjectEpoch(ctx, w.state, w.project)
	if err != nil || epoch > 0 {
		return epoch, err
	}
	_, err = w.connect(ctx, false)
	return w.epoch, err
}

func (w *ProjectMonitor) connect(ctx context.Context, reactivate bool) (Enrollment, error) {
	enrollment, _, err := w.read(ctx)
	if err != nil {
		return Enrollment{}, err
	}
	w.epoch, err = ActivateProjectMonitor(ctx, w.state, w.project, reactivate)
	return enrollment, err
}

// VerifyEnrollment checks the dedicated role and its current enrollment without registering a worker.
func (w *ProjectMonitor) VerifyEnrollment(ctx context.Context) (Enrollment, error) {
	enrollment, _, err := w.read(ctx)
	return enrollment, err
}

// ActivateProjectMonitor registers a verified installation. An application can
// pass its account registration transaction so both changes become visible together.
func ActivateProjectMonitor(ctx context.Context, state interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, project supabaseproject.Project, reactivate bool) (int64, error) {
	var epoch int64
	err := state.QueryRow(ctx, `
		INSERT INTO pooltune_worker.projects(project_id, monitor_epoch) VALUES ($1, 1)
		ON CONFLICT (project_id) DO UPDATE
		SET monitor_epoch = CASE WHEN projects.monitor_epoch < 0 THEN 1 - projects.monitor_epoch ELSE projects.monitor_epoch END
		WHERE projects.monitor_epoch IS NOT NULL AND ($2::boolean OR projects.monitor_epoch>0)
		RETURNING monitor_epoch
	`, project.ID(), reactivate).Scan(&epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrProjectDisconnected
	}
	return epoch, err
}

// ProjectMonitorEpoch is read before connecting to Supabase. The runtime keeps
// this value so a previous process cannot resume after a disconnect/reconnect.
func ProjectMonitorEpoch(ctx context.Context, state *pgx.Conn, project supabaseproject.Project) (int64, error) {
	var epoch int64
	err := state.QueryRow(ctx, "SELECT monitor_epoch FROM pooltune_worker.projects WHERE project_id=$1 AND monitor_epoch>0", project.ID()).Scan(&epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrProjectDisconnected
	}
	return epoch, err
}

// HostedProjectEpoch returns zero only for a new, explicitly configured installation.
func HostedProjectEpoch(ctx context.Context, state *pgx.Conn, project supabaseproject.Project) (int64, error) {
	var epoch *int64
	err := state.QueryRow(ctx, "SELECT monitor_epoch FROM pooltune_worker.projects WHERE project_id=$1", project.ID()).Scan(&epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if epoch == nil || *epoch <= 0 {
		return 0, ErrProjectDisconnected
	}
	return *epoch, nil
}

func (w *ProjectMonitor) poll(ctx context.Context, at time.Time) (PollResult, error) {
	if w.epoch <= 0 {
		return PollResult{}, ErrProjectDisconnected
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, min(2*time.Second, w.policy.Interval))
	defer cancel()
	result, err := recordProjectPoll(ctx, w.state, w.project.ID(), w.policy, at.UTC().Truncate(time.Microsecond), w.epoch, w.read)
	result.TotalMS = elapsedMS(started)
	return result, err
}

func (w *ProjectMonitor) Run(ctx context.Context, epoch int64, onPoll func(PollResult) error) error {
	w.epoch = epoch
	return runPolls(ctx, w.policy.Interval, w.poll, onPoll)
}

func DisconnectProjectMonitor(ctx context.Context, state *pgx.Conn, project supabaseproject.Project) error {
	return pgx.BeginFunc(ctx, state, func(tx pgx.Tx) error {
		return DisconnectProjectMonitorTx(ctx, tx, project)
	})
}

// DisconnectProjectMonitorTx retires jobs in the caller's account transaction.
func DisconnectProjectMonitorTx(ctx context.Context, tx pgx.Tx, project supabaseproject.Project) error {
	tag, err := tx.Exec(ctx, `
			INSERT INTO pooltune_worker.projects(project_id, monitor_epoch) VALUES ($1, -1)
			ON CONFLICT (project_id) DO UPDATE
			SET monitor_epoch = -abs(projects.monitor_epoch), last_poll = NULL, evidence = '{}'
			WHERE projects.monitor_epoch IS NOT NULL
		`, project.ID())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrProjectDisconnected
	}
	_, err = tx.Exec(ctx, `
			UPDATE pooltune_worker.investigations
			SET finished_at=clock_timestamp(), lease_until=NULL, result='{"outcome":"project_disconnected"}'
			WHERE project_id=$1 AND finished_at IS NULL
		`, project.ID())
	return err
}

func retireChangedEnrollments(ctx context.Context, tx pgx.Tx, enrollment Enrollment) error {
	roles := make([]uint32, 0, len(enrollment.Queries))
	queries := make([]int64, 0, len(enrollment.Queries))
	objectives := make([]float64, 0, len(enrollment.Queries))
	var database uint32
	for _, objective := range enrollment.Queries {
		database = objective.Database
		roles = append(roles, objective.Role)
		queries = append(queries, objective.Query)
		objectives = append(objectives, objective.MeanMS)
	}
	_, err := tx.Exec(ctx, `
		WITH stale AS (
			SELECT j.id, CASE WHEN e.role_oid IS NULL THEN 'query_unenrolled'
				ELSE 'query_objective_changed' END AS outcome
			FROM pooltune_worker.investigations j
			LEFT JOIN unnest($3::oid[], $4::bigint[], $5::double precision[]) AS e(role_oid,query_id,objective_ms)
				ON j.database_oid=$2 AND e.role_oid=j.role_oid AND e.query_id=j.query_id
			WHERE j.project_id=$1 AND j.finished_at IS NULL
			  AND (e.role_oid IS NULL OR j.evidence->'objective_ms' IS DISTINCT FROM to_jsonb(e.objective_ms))
		)
		UPDATE pooltune_worker.investigations j
		SET finished_at=clock_timestamp(), lease_until=NULL,
			result=jsonb_build_object('outcome', stale.outcome)
		FROM stale WHERE j.id=stale.id
	`, enrollment.Project, database, roles, queries, objectives)
	return err
}
