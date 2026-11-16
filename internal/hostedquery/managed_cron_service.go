package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
)

// Run collects only the approved scheduling windows. Action and model calls are
// queued for the ordinary shared dispatcher, separate from this collector.
func (m *ManagedProjectCron) Run(ctx context.Context, stateConfig *pgx.ConnConfig, project supabaseproject.Project, epoch int64) error {
	if m == nil || m.Config == nil || stateConfig == nil {
		return errors.New("scheduling service needs native and durable state routes")
	}
	state, err := pgx.ConnectConfig(ctx, stateConfig.Copy())
	if err != nil {
		return err
	}
	defer closeVacuumConnection(state)
	owner, err := supabaseproject.ConnectRepair(ctx, m.Config)
	if err != nil {
		return err
	}
	defer closeVacuumConnection(owner)
	if err := project.VerifyManagedRepair(ctx, owner); err != nil {
		return err
	}
	// The supervisor removes old witnesses before starting the dispatcher.
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var lastSweep time.Time
	for ctx.Err() == nil {
		current, err := ProjectMonitorEpoch(ctx, state, project)
		if err != nil {
			return err
		}
		if current != epoch {
			return ErrProjectDisconnected
		}
		if time.Since(lastSweep) >= 10*time.Second {
			pending, err := PendingManagedCronWitnesses(ctx, state, project.ID())
			if err != nil {
				return err
			}
			for _, witness := range pending {
				if time.Now().Before(witness.Expires) {
					continue
				}
				if err := RemoveManagedCronWitness(ctx, owner, witness); err != nil {
					return err
				}
				if err := SettleManagedCronWitness(ctx, state, witness); err != nil {
					return err
				}
			}
			lastSweep = time.Now()
		}
		for _, original := range m.Enrollments {
			enrolled := original
			pending, err := pendingManagedCronEnrollment(ctx, state, project.ID(), original.Target.JobID)
			if err != nil {
				return err
			}
			if pending != nil {
				enrolled = *pending
			}
			if m.Paused && pending == nil {
				continue
			}
			if !managedCronObservationDue(time.Now(), enrolled.Window) {
				continue
			}
			observation, err := ObserveManagedCron(ctx, owner, enrolled)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				continue
			}
			retention := min(4*time.Hour, 4*time.Duration(enrolled.Window.EveryMinutes)*time.Minute)
			if err := SaveManagedCronObservation(ctx, state, project.ID(), epoch, enrolled.Target.JobID, retention, observation); err != nil {
				return err
			}
			if pending == nil {
				if err := enqueueManagedCronCause(ctx, state, project.ID(), epoch, enrolled, observation); err != nil {
					return err
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	return ctx.Err()
}

func managedCronObservationDue(now time.Time, w ProjectCronWindowPlan) bool {
	duration := time.Duration(w.EveryMinutes) * time.Minute
	if duration <= 0 {
		return false
	}
	elapsed := now.Sub(now.UTC().Truncate(duration))
	// Include the subsequent second so the native timestamp can be matched to
	// the existing one-second alignment tolerance. No missing window is invented.
	for _, start := range []time.Duration{time.Duration(w.CurrentMinute) * time.Minute, time.Duration(w.CurrentMinute)*time.Minute + 30*time.Second, time.Duration(w.ProposedMinute) * time.Minute} {
		if elapsed >= start-time.Second && elapsed <= start+23*time.Second {
			return true
		}
	}
	return false
}

func pendingManagedCronEnrollment(ctx context.Context, state *pgx.Conn, project string, job int64) (*ManagedCronEnrollment, error) {
	var raw []byte
	err := state.QueryRow(ctx, `SELECT intent->'Followup' FROM pooltune_worker.owned_project_actions WHERE project_id=$1 AND operation='cron' AND outcome='pending' AND intent->>'RunID' LIKE 'managed-cron-%' AND (intent#>>'{Enrollment,Target,JobID}')::bigint=$2`, project, job).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var enrolled *ManagedCronEnrollment
	if err := json.Unmarshal(raw, &enrolled); err != nil {
		return nil, err
	}
	return enrolled, nil
}

func enqueueManagedCronCause(ctx context.Context, state *pgx.Conn, project string, epoch int64, enrolled ManagedCronEnrollment, latest ManagedCronObservation) error {
	duration := time.Duration(enrolled.Window.EveryMinutes) * time.Minute
	cycle := latest.At.UTC().Truncate(duration)
	settled := cycle.Add(max(time.Duration(enrolled.Window.CurrentMinute)*time.Minute+51*time.Second, time.Duration(enrolled.Window.ProposedMinute)*time.Minute+21*time.Second))
	if latest.At.Before(settled) || !latest.At.Before(cycle.Add(duration-25*time.Second)) {
		return nil
	}
	observations, err := ReadManagedCronObservations(ctx, state, project, epoch, enrolled.Target.JobID, cycle.Add(-2*duration))
	if err != nil {
		return err
	}
	var chosen ManagedCronQueries
	var valid bool
	original, alternate, _ := enrolled.Window.schedules()
	for _, o := range observations {
		q, err := SelectManagedCronQueries(o)
		if err != nil {
			continue
		}
		periods, err := BuildManagedCronPeriods(observations, q, enrolled.Window, enrolled.Target.JobID, cycle.Add(-2*duration), false)
		if err != nil {
			continue
		}
		if ManagedCronCause(ManagedCronEvidence{JobID: enrolled.Target.JobID, Original: original, Alternate: alternate, Window: enrolled.Window, Generation: latest.Generation, Periods: periods, ApplicationRequestVisibility: "unobserved"}) {
			chosen = q
			valid = true
			break
		}
	}
	if !valid {
		return nil
	}
	raw, err := json.Marshal(managedCronQueue{enrolled.Target.JobID, cycle})
	if err != nil {
		return err
	}
	return pgx.BeginTxFunc(ctx, state, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := lockOwnedCronCheckpointEpoch(ctx, tx, project, epoch); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence)
 SELECT $1,$2,$3,$4,$5::jsonb WHERE NOT EXISTS(SELECT FROM pooltune_worker.investigations WHERE project_id=$1 AND (finished_at IS NULL OR evidence=$5::jsonb))
 AND NOT EXISTS(SELECT FROM pooltune_worker.owned_project_actions WHERE project_id=$1 AND outcome='pending')
 AND NOT EXISTS(SELECT FROM pooltune_worker.owned_index_actions WHERE project_id=$1 AND outcome='pending')`, project, chosen.Target.Database, chosen.Target.Role, chosen.Target.Query, string(raw))
		return err
	})
}

// Removing the last selection must not leave a delayed pending action blocking
// every other repair. Wake only its existing claim for a no-mutation settlement.
func (m *ManagedProjectCron) wakeInactive(ctx context.Context, state *pgx.Conn, project string, epoch int64) error {
	rows, err := state.Query(ctx, `SELECT intent FROM pooltune_worker.owned_project_actions WHERE project_id=$1 AND operation='cron' AND outcome='pending' AND intent->>'RunID' LIKE 'managed-cron-%'`, project)
	if err != nil {
		return err
	}
	var jobs []int64
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var a managedCronAction
		if err := json.Unmarshal(raw, &a); err != nil {
			rows.Close()
			return err
		}
		current, found := m.enrollment(a.Enrollment.Target.JobID)
		if !found || m.Paused || !sameManagedCronEnrollment(current, a.Enrollment) {
			jobs = append(jobs, a.Enrollment.Target.JobID)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(jobs) == 0 {
		return nil
	}
	return pgx.BeginTxFunc(ctx, state, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := lockOwnedCronCheckpointEpoch(ctx, tx, project, epoch); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE pooltune_worker.investigations SET not_before=LEAST(not_before,clock_timestamp()) WHERE project_id=$1 AND finished_at IS NULL AND (evidence->>'managed_cron_job_id')::bigint=ANY($2::bigint[])`, project, jobs)
		return err
	})
}
