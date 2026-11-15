package hostedapp

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"pooltune/internal/hostedquery"
	"pooltune/internal/labdb"

	"github.com/jackc/pgx/v5"
)

type projectProcess struct {
	epoch  int64
	cancel context.CancelFunc
}

// RunWorkers reconciles active registrations after deploys and on connection
// changes. A database session lock avoids overlapping workers during redeploy.
func (a *App) RunWorkers(ctx context.Context) {
	processes := make(map[string]projectProcess)
	var workers sync.WaitGroup
	defer func() {
		for _, process := range processes {
			process.cancel()
		}
		workers.Wait()
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		check, cancel := context.WithTimeout(ctx, 2*time.Second)
		active, err := a.projects.active(check)
		cancel()
		if err == nil {
			desired := make(map[string]int64, len(active))
			for _, row := range active {
				desired[row.ID] = row.Epoch
			}
			for id, running := range processes {
				if desired[id] != running.epoch {
					running.cancel()
					delete(processes, id)
				}
			}
			for _, row := range active {
				if _, found := processes[row.ID]; found {
					continue
				}
				worker, cancel := context.WithCancel(ctx)
				processes[row.ID] = projectProcess{epoch: row.Epoch, cancel: cancel}
				workers.Go(func() { a.superviseProject(worker, row) })
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) superviseProject(ctx context.Context, row projectRecord) {
	for ctx.Err() == nil {
		err := a.runProject(ctx, row)
		if errors.Is(err, hostedquery.ErrProjectDisconnected) || ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Warn("preview monitor restarting", "installation", row.ID, "status", "temporarily_unavailable")
		}
		if !wait(ctx, 3*time.Second) {
			return
		}
	}
}

func (a *App) runProject(ctx context.Context, row projectRecord) error {
	identity, err := row.identity()
	if err != nil {
		return err
	}
	credentials, err := a.vault.open(row, row.Secret)
	if err != nil {
		return err
	}
	config, err := identity.Config(credentials.Password, []byte(credentials.CA))
	if err != nil {
		return err
	}
	logs, err := hostedquery.NewTrafficPlanReader(credentials.LogsToken)
	if err != nil {
		return err
	}
	setup, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	state, err := a.projects.db.Acquire(setup)
	if err != nil {
		return err
	}
	defer state.Release()
	var locked bool
	if err := state.QueryRow(setup, `SELECT pg_try_advisory_lock(hashtextextended($1,761994345007))`, identity.ID()).Scan(&locked); err != nil || !locked {
		// A canceled lock query can leave a lock on an otherwise idle session.
		if err != nil {
			labdb.Close(state.Conn())
		}
		return err
	}
	defer func() {
		unlock, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if _, err := state.Exec(unlock, `SELECT pg_advisory_unlock(hashtextextended($1,761994345007))`, identity.ID()); err != nil {
			labdb.Close(state.Conn())
		}
	}()
	epoch, err := hostedquery.ProjectMonitorEpoch(setup, state.Conn(), identity)
	if err != nil {
		return err
	}
	if epoch != row.Epoch {
		return hostedquery.ErrProjectDisconnected
	}
	target, err := pgx.ConnectConfig(setup, config)
	if err != nil {
		return err
	}
	defer labdb.Close(target)
	monitor, err := hostedquery.NewProjectMonitor(target, state.Conn(), identity, a.policy)
	if err != nil {
		return err
	}
	if a.investigate == nil {
		cancel()
		return monitor.Run(ctx, epoch, func(hostedquery.PollResult) error { return nil })
	}
	var owned *hostedquery.OwnedProjectIndex
	if a.ownedIndex.Matches(identity) && a.ownedIndex.Discover == credentials.Automatic && slices.Contains(credentials.Capabilities, "index") {
		fixture := *a.ownedIndex
		fixture.Paused = credentials.RepairsPaused
		fixture.AdmitModel = func(ctx context.Context) error {
			return a.projects.admitModel(ctx, row.ID, epoch)
		}
		owned = &fixture
	}
	var repair *hostedquery.OwnedProjectRepair
	if a.ownedRepair.Matches(identity) && a.ownedRepair.Automatic == credentials.Automatic &&
		!credentials.RepairsPaused && slices.Contains(credentials.Capabilities, a.ownedRepair.Operation) {
		fixture := *a.ownedRepair
		fixture.AdmitModel = func(ctx context.Context) error {
			return a.projects.admitModel(ctx, row.ID, epoch)
		}
		repair = &fixture
	}
	queue, err := a.projects.db.Acquire(setup)
	if err != nil {
		return err
	}
	defer queue.Release()
	cancel()
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	finished := make(chan error, 2)
	go func() { finished <- monitor.Run(ctx, epoch, func(hostedquery.PollResult) error { return nil }) }()
	go func() {
		for ctx.Err() == nil {
			result, err := hostedquery.DiagnoseHostedProjectWithRepair(ctx, queue.Conn(), config, identity, epoch, a.projectInvestigator(row.ID, epoch), logs, owned, repair)
			if err != nil && queue.Conn().IsClosed() {
				finished <- err
				return
			}
			if result != nil {
				slog.Info("preview investigation saved", "installation", row.ID, "outcome", result.Outcome, "job", result.JobID)
			}
			if !wait(ctx, a.queueInterval) {
				break
			}
		}
		finished <- ctx.Err()
	}()
	err = <-finished
	stop()
	<-finished
	return err
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (a *App) projectInvestigator(id string, epoch int64) hostedquery.EnrolledLookupInvestigator {
	return func(ctx context.Context, runID string, table uint32, column int16, at time.Time,
		evidence hostedquery.LookupEvidence, admit func(context.Context) error) (*hostedquery.LookupDiagnosis, error) {
		return a.investigate(ctx, runID, table, column, at, evidence, func(check context.Context) error {
			if err := a.projects.admitModel(check, id, epoch); err != nil {
				return err
			}
			// Keep the shared epoch, job and evidence check closest to model admission.
			return admit(check)
		})
	}
}
