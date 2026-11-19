package hostedapp

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"pooltune/internal/hostedapp/auth"
	"pooltune/internal/hostedquery"
	"pooltune/internal/labdb"
	"pooltune/internal/supabaseproject"

	"github.com/jackc/pgx/v5"
)

// The epoch in the rendered form fences an old tab, including a disconnect and
// reconnect of the same project. A repeated request never toggles the new state.
func (a *App) controlProject(w http.ResponseWriter, r *http.Request, user auth.User) (projectRecord, projectSecrets, bool) {
	row, ok := a.ownedProject(w, r, user)
	if !ok {
		return row, projectSecrets{}, false
	}
	if r.ParseForm() != nil || row.Epoch <= 0 || r.PostForm.Get("epoch") != strconv.FormatInt(row.Epoch, 10) {
		http.Error(w, "The project changed. Refresh the page and try again.", http.StatusConflict)
		return row, projectSecrets{}, false
	}
	secret, err := a.vault.open(row, row.Secret)
	if err != nil || secret.DisconnectPending {
		http.Error(w, "This connection is unavailable.", http.StatusConflict)
		return row, projectSecrets{}, false
	}
	return row, secret, true
}

func (a *App) changeAgentMode(w http.ResponseWriter, r *http.Request, user auth.User) {
	row, secret, ok := a.controlProject(w, r, user)
	if !ok {
		return
	}
	mode := r.PostForm.Get("mode")
	if mode != "pause" && mode != "resume" {
		http.Error(w, "Choose pause or resume.", http.StatusBadRequest)
		return
	}
	paused := mode == "pause"
	if row.AgentPaused && !paused {
		if err := a.finishPauseCleanup(r.Context(), row, secret); err != nil {
			http.Error(w, "The agent is paused. Temporary scheduler cleanup must finish before monitoring resumes. Retry shortly.", http.StatusConflict)
			return
		}
	}
	if row.AgentPaused != paused {
		if err := a.projects.setAgentPaused(r.Context(), row, paused); err != nil {
			http.Error(w, "The agent state changed or a repair is still finishing. Refresh and try again.", http.StatusConflict)
			return
		}
		row.Epoch++
		row.AgentPaused = paused
	}
	if paused {
		if err := a.finishPauseCleanup(r.Context(), row, secret); err != nil {
			http.Error(w, "The agent is paused. Pooltune will retry temporary scheduler cleanup before monitoring can resume.", http.StatusConflict)
			return
		}
	}
	http.Redirect(w, r, "/app/projects/"+row.ID, http.StatusSeeOther)
}

func (s projectStore) setAgentPaused(ctx context.Context, row projectRecord, paused bool) error {
	project, err := row.identity()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE pooltune_app.projects
			SET agent_paused=$4,pause_cleanup=$4,epoch=epoch+1
			WHERE id=$1 AND owner_id=$2 AND epoch=$3 AND epoch>0 AND agent_paused<>$4
			AND ($4 OR (NOT pause_cleanup AND NOT supabase_blocked))`, row.ID, row.Owner, row.Epoch, paused)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return hostedquery.ErrProjectDisconnected
		}
		// Polls, DDL, and admissions fence on this same row. Persisted pending
		// repair journals are left intact for reconciliation at resume.
		tag, err = tx.Exec(ctx, `UPDATE pooltune_worker.projects SET monitor_epoch=CASE WHEN $3 THEN -($2::bigint+1) ELSE $2::bigint+1 END, agent_paused=$3, evidence='{}'
			WHERE project_id=$1 AND monitor_epoch=CASE WHEN agent_paused THEN -$2::bigint ELSE $2::bigint END AND agent_paused<>$3`, project.ID(), row.Epoch, paused)
		if err == nil && tag.RowsAffected() != 1 {
			return hostedquery.ErrProjectDisconnected
		}
		return err
	})
}

// Only temporary scheduler witnesses need a customer connection while paused.
// The lease is shared with the supervisor, so cancellation and failover drain
// the previous worker before cleanup. An unavailable provider leaves a retry.
func (a *App) finishPauseCleanup(ctx context.Context, row projectRecord, secret projectSecrets) error {
	project, err := row.identity()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	return pgx.BeginFunc(ctx, a.projects.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,761994345007))`, project.ID()); err != nil {
			return err
		}
		var pending bool
		if err := tx.QueryRow(ctx, `SELECT pause_cleanup FROM pooltune_app.projects WHERE id=$1 AND owner_id=$2 AND epoch=$3 AND agent_paused`, row.ID, row.Owner, row.Epoch).Scan(&pending); err != nil {
			return err
		}
		if !pending {
			return nil
		}
		if err := a.cleanupProjectWitnesses(ctx, row, secret); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE pooltune_app.projects SET pause_cleanup=false WHERE id=$1 AND epoch=$2 AND agent_paused`, row.ID, row.Epoch)
		return err
	})
}

func (a *App) revertChange(w http.ResponseWriter, r *http.Request, user auth.User) {
	row, secret, ok := a.controlProject(w, r, user)
	if !ok {
		return
	}
	job, err := strconv.ParseInt(r.PathValue("job"), 10, 64)
	if err != nil || job <= 0 || row.SupabaseBlocked || (secret.RepairPassword == "" && row.SupabaseConnection == "") {
		http.Error(w, "This change is unavailable for reversion.", http.StatusConflict)
		return
	}
	project, err := row.identity()
	if err != nil {
		http.Error(w, "Project unavailable.", http.StatusConflict)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	// Serialize with account permission changes and the worker's native DDL
	// fence. Resolve customer credentials only for an eligible recorded job.
	err = pgx.BeginFunc(ctx, a.projects.db, func(tx pgx.Tx) error {
		var current int64
		if err := tx.QueryRow(ctx, `SELECT epoch FROM pooltune_app.projects WHERE owner_id=$1 AND id=$2 AND epoch=$3 AND NOT supabase_blocked FOR UPDATE NOWAIT`, row.Owner, row.ID, row.Epoch).Scan(&current); err != nil {
			return err
		}
		return hostedquery.RevertManagedProjectIndex(ctx, tx, a.projects.db, project.ID(), row.Epoch, job, func() (context.Context, *pgx.Conn, error) {
			bound, cfg, err := a.repairContext(ctx, row, secret)
			if err != nil {
				return bound, nil, err
			}
			target, err := supabaseproject.ConnectRepair(bound, cfg)
			if err != nil {
				return bound, nil, err
			}
			if err := project.VerifyManagedRepair(bound, target); err != nil {
				labdb.Close(target)
				return bound, nil, err
			}
			return bound, target, nil
		})
	})
	if err != nil {
		http.Error(w, "Could not confirm this revert. The change may be ineligible, the index may have changed, or the database may be busy. Refresh before retrying.", http.StatusConflict)
		return
	}
	path := "/app/projects/" + row.ID + "?section=changes"
	if before, err := strconv.ParseInt(r.PostFormValue("before"), 10, 64); err == nil && before > 0 {
		path += "&before=" + strconv.FormatInt(before, 10)
	}
	http.Redirect(w, r, path+"#run-"+strconv.FormatInt(job, 10), http.StatusSeeOther)
}
