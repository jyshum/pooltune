package hostedapp

import (
	"context"
	"errors"
	"time"

	"pooltune/internal/hostedquery"
	"pooltune/internal/labdb"
	"pooltune/internal/supabaseproject"
)

// Disconnect first fences all repair claims. A failed cleanup never restores
// permissions, and keeps the encrypted connection solely for the next cleanup.
func (a *App) disconnectAfterCleanup(ctx context.Context, row projectRecord, cleanup func(context.Context, projectRecord, projectSecrets) error) error {
	if row.Epoch <= 0 {
		return a.projects.disconnect(ctx, row)
	}
	secret, err := a.vault.open(row, row.Secret)
	if err != nil {
		return err
	}
	if !secret.DisconnectPending {
		secret.RepairsPaused, secret.DisconnectPending = true, true
		sealed, err := a.vault.seal(row, secret)
		if err != nil {
			return err
		}
		if err := a.projects.replaceActivePermissions(ctx, row, sealed); err != nil {
			return err
		}
		row.Epoch++
		row.Secret = sealed
	}
	a.workerMu.Lock()
	if process, ok := a.workerStops[row.ID]; ok && process.epoch <= row.Epoch {
		process.cancel()
	}
	a.workerMu.Unlock()
	identity, err := row.identity()
	if err != nil {
		return err
	}
	// The same lock held by the monitor prevents a committing witness from
	// racing the final cleanup. New workers refuse DisconnectPending above.
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	conn, err := a.projects.db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1,761994345007))`, identity.ID()); err != nil {
		labdb.Close(conn.Conn())
		return err
	}
	defer func() {
		unlock, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if _, err := conn.Exec(unlock, `SELECT pg_advisory_unlock(hashtextextended($1,761994345007))`, identity.ID()); err != nil {
			labdb.Close(conn.Conn())
		}
	}()
	var current bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pooltune_app.projects WHERE id=$1 AND owner_id=$2 AND epoch=$3 AND epoch>0)`, row.ID, row.Owner, row.Epoch).Scan(&current); err != nil {
		return err
	}
	if !current {
		return hostedquery.ErrProjectDisconnected
	}
	if err := cleanup(ctx, row, secret); err != nil {
		return err
	}
	return a.projects.disconnect(ctx, row)
}

func (a *App) cleanupProjectWitnesses(ctx context.Context, row projectRecord, secret projectSecrets) error {
	state, err := a.projects.db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer state.Release()
	project, err := row.identity()
	if err != nil {
		return err
	}
	witnesses, err := hostedquery.PendingManagedCronWitnesses(ctx, state.Conn(), project.ID())
	if err != nil || len(witnesses) == 0 {
		return err
	}
	if secret.RepairPassword == "" && row.SupabaseConnection == "" {
		return errors.New("cleanup requires the retained repair connection")
	}
	ctx, cfg, err := a.repairContext(ctx, row, secret)
	if err != nil {
		return err
	}
	db, err := supabaseproject.ConnectRepair(ctx, cfg)
	if err != nil {
		return err
	}
	defer labdb.Close(db)
	if err := project.VerifyManagedRepair(ctx, db); err != nil {
		return err
	}
	for len(witnesses) > 0 {
		for _, witness := range witnesses {
			if err := hostedquery.RemoveManagedCronWitness(ctx, db, witness); err != nil {
				return err
			}
			if err := hostedquery.SettleManagedCronWitness(ctx, state.Conn(), witness); err != nil {
				return err
			}
		}
		witnesses, err = hostedquery.PendingManagedCronWitnesses(ctx, state.Conn(), project.ID())
		if err != nil {
			return err
		}
	}
	return nil
}
