package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
)

// RefreshMemoryAllowances keeps only verified, still-enrolled repairs active.
// Failure is conservative: the installed function automatically falls back
// when the existing sixty-second allowance expires.
func (m *ManagedProjectIndex) RefreshMemoryAllowances(ctx context.Context, state *pgx.Conn, project supabaseproject.Project, epoch int64) error {
	if m == nil || m.Paused || !slices.Contains(m.Capabilities, "memory") || len(m.MemoryEnrollments) == 0 || m.MemoryHeadroom == nil {
		return nil
	}
	if time.Since(m.memoryRefreshedAt) < 30*time.Second {
		return nil
	}
	m.memoryRefreshedAt = time.Now()
	rows, err := state.Query(ctx, `SELECT intent FROM pooltune_worker.owned_project_actions WHERE project_id=$1 AND operation='memory' AND outcome='improved_tested_inputs' AND intent->>'run_id' LIKE 'managed-memory-%' ORDER BY job_id DESC LIMIT 32`, project.ID())
	if err != nil {
		return err
	}
	var changes []ManagedMemoryChange
	seen := map[uint32]bool{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var j managedMemoryJournal
		if json.Unmarshal(raw, &j) != nil || j.Applied == nil {
			continue
		}
		target := j.Function.Target
		if seen[target.FunctionOID] {
			continue
		}
		seen[target.FunctionOID] = true
		for _, e := range m.MemoryEnrollments {
			if e.Target == target && e.Generation == j.Function.Snapshot.Generation && e.Fingerprint == j.Function.CatalogFingerprint {
				changes = append(changes, *j.Applied)
				break
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(changes) == 0 {
		return nil
	}
	h, err := m.MemoryHeadroom(ctx)
	if err != nil {
		return err
	}
	if h.ProjectRef != project.Ref() || h.MemoryAllowanceSlots(time.Now()) < 1 {
		return supabaseproject.ErrMemoryHeadroomUnavailable
	}
	owner, err := supabaseproject.ConnectRepair(ctx, m.Config)
	if err != nil {
		return err
	}
	defer closeVacuumConnection(owner)
	if err := project.VerifyManagedRepair(ctx, owner); err != nil {
		return err
	}
	return pgx.BeginTxFunc(ctx, state, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var active string
		if err := tx.QueryRow(ctx, `SELECT project_id FROM pooltune_worker.projects WHERE project_id=$1 AND monitor_epoch=$2 FOR UPDATE`, project.ID(), epoch).Scan(&active); err != nil {
			return err
		}
		for _, change := range changes {
			if err := RefreshManagedMemoryAllowance(ctx, owner, change, h); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		return nil
	})
}
