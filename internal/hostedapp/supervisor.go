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
	"pooltune/internal/supabaseproject"

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
				a.workerMu.Lock()
				if a.workerStops == nil {
					a.workerStops = make(map[string]projectProcess)
				}
				a.workerStops[row.ID] = processes[row.ID]
				a.workerMu.Unlock()
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
	if credentials.DisconnectPending {
		return hostedquery.ErrProjectDisconnected
	}
	if row.AgentPaused {
		if err := a.finishPauseCleanup(ctx, row, credentials); err != nil {
			return err
		}
		return hostedquery.ErrProjectDisconnected
	}
	if row.SupabaseConnection != "" {
		if _, err := a.projectSupabaseToken(ctx, row); err != nil {
			if errors.Is(err, errSupabaseReconnect) || errors.Is(err, errSupabaseProjectAccess) {
				_ = a.blockSupabaseProject(ctx, row)
			}
			return err
		}
		var stop context.CancelFunc
		ctx, stop = context.WithCancel(ctx)
		defer stop()
		go func(ctx context.Context) {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					check, cancel := context.WithTimeout(ctx, 12*time.Second)
					_, err := a.projectSupabaseToken(check, row)
					if err != nil {
						if errors.Is(err, errSupabaseReconnect) || errors.Is(err, errSupabaseProjectAccess) {
							_ = a.blockSupabaseProject(check, row)
						}
						cancel()
						stop()
						return
					}
					cancel()
				}
			}
		}(ctx)
	}
	config, err := identity.Config(credentials.Password, []byte(credentials.CA))
	if err != nil {
		return err
	}
	logs, err := hostedquery.NewTrafficPlanReader(credentials.LogsToken)
	if row.SupabaseConnection != "" {
		logs = hostedquery.NewOAuthTrafficPlanReader(row.Ref, func(ctx context.Context) (string, error) { return a.projectSupabaseToken(ctx, row) })
		err = nil
	}
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
	var cron *hostedquery.OwnedProjectCron
	if a.ownedCron.Matches(identity) && (a.ownedCron.QueryID == 0) == credentials.Automatic &&
		!credentials.RepairsPaused && slices.Contains(credentials.Capabilities, "cron") {
		fixture := *a.ownedCron
		fixture.AdmitModel = func(ctx context.Context) error {
			return a.projects.admitModel(ctx, row.ID, epoch)
		}
		cron = &fixture
	}
	var managed *hostedquery.ManagedProjectIndex
	if (credentials.RepairPassword != "" || row.SupabaseConnection != "") && a.investigateRepair != nil {
		var repairConfig *pgx.ConnConfig
		ctx, repairConfig, err = a.repairContext(ctx, row, credentials)
		if err != nil {
			return err
		}
		managed = &hostedquery.ManagedProjectIndex{
			Config: repairConfig, Investigate: a.investigateRepair, Paused: credentials.RepairsPaused,
			Capabilities: slices.Clone(credentials.Capabilities),
			AdmitModel:   func(ctx context.Context) error { return a.projects.admitModel(ctx, row.ID, epoch) },
		}
		managed.Cron = &hostedquery.ManagedProjectCron{Config: repairConfig, Investigate: a.investigateRepair, AdmitModel: managed.AdmitModel, Paused: credentials.RepairsPaused || !slices.Contains(credentials.Capabilities, "cron")}
		for _, enrollment := range credentials.Enrollments {
			target := enrollment.Target
			if target.Kind == "cron" && enrollment.Window != nil {
				managed.Cron.Enrollments = append(managed.Cron.Enrollments, hostedquery.ManagedCronEnrollment{Target: hostedquery.ProjectCronTarget{Project: identity.ID(), DatabaseOID: target.DatabaseOID, OwnerOID: target.OwnerOID, JobID: target.JobID, Name: target.Name, CommandDigest: target.CommandDigest}, Fingerprint: target.Fingerprint, Generation: target.Generation, Schedule: target.Schedule, Window: *enrollment.Window})
			}
			if target.Kind != "memory" {
				continue
			}
			managed.MemoryEnrollments = append(managed.MemoryEnrollments, hostedquery.ManagedMemoryEnrollment{
				Target: hostedquery.ProjectFunctionMemoryTarget{Project: identity.ID(), DatabaseOID: target.DatabaseOID, FunctionOID: target.ObjectOID, Schema: target.Schema, Function: target.Name}, Fingerprint: target.Fingerprint, Generation: target.Generation,
			})
		}
		if credentials.LogsToken != "" || row.SupabaseConnection != "" {
			reader, err := supabaseproject.NewMemoryMetricsReader(identity.Ref(), credentials.LogsToken)
			if row.SupabaseConnection != "" {
				reader = supabaseproject.NewOAuthMemoryMetricsReader(row.Ref, func(ctx context.Context) (string, error) { return a.projectSupabaseToken(ctx, row) })
				err = nil
			}
			if err != nil {
				return err
			}
			managed.MemoryHeadroom = func(ctx context.Context) (supabaseproject.MemoryHeadroom, error) {
				return reader.Read(ctx, identity.Ref())
			}
		}

	}
	queue, err := a.projects.db.Acquire(setup)
	if err != nil {
		return err
	}
	defer queue.Release()
	// Reconcile old diagnostic witnesses before any new dispatcher can create
	// one. The supervisor lock keeps a previous process from overlapping.
	if err := a.cleanupProjectWitnesses(setup, row, credentials); err != nil {
		return err
	}
	cancel()
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	workerCount := 2
	if managed != nil && managed.Cron != nil && len(managed.Cron.Enrollments) > 0 {
		workerCount++
	}
	finished := make(chan error, workerCount)
	if workerCount == 3 {
		go func() {
			for ctx.Err() == nil {
				// A failed scheduling observation cannot invent a completed
				// period, and must not stop the independent query monitor.
				if err := managed.Cron.Run(ctx, queue.Conn().Config(), identity, epoch); errors.Is(err, hostedquery.ErrProjectDisconnected) {
					break
				}
				if !wait(ctx, 3*time.Second) {
					break
				}
			}
			finished <- ctx.Err()
		}()
	}
	go func() { finished <- monitor.Run(ctx, epoch, func(hostedquery.PollResult) error { return nil }) }()
	go func() {
		for ctx.Err() == nil {
			// A failed refresh leaves every installed function at its original
			// memory allowance once its short lease expires.
			if row.SupabaseConnection != "" {
				if _, err := a.projectSupabaseToken(ctx, row); err != nil {
					finished <- err
					return
				}
			}
			if managed != nil {
				_ = managed.RefreshMemoryAllowances(ctx, queue.Conn(), identity, epoch)
			}
			result, err := hostedquery.DiagnoseHostedProjectWithRepair(ctx, queue.Conn(), config, identity, epoch, a.projectInvestigator(row.ID, epoch), logs, owned, repair, cron, managed)
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
	for range workerCount - 1 {
		<-finished
	}
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
