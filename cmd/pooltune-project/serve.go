package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"pooltune/internal/hostedquery"
	"pooltune/internal/labdb"
	"pooltune/internal/supabaseproject"

	"github.com/jackc/pgx/v5"
)

func serveProject(ctx context.Context, monitor *hostedquery.ProjectMonitor, queue *pgx.Conn,
	target *pgx.ConnConfig, project supabaseproject.Project, epoch int64,
	queueInterval time.Duration, investigate hostedquery.EnrolledLookupInvestigator, encoder *json.Encoder) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var logLock sync.Mutex
	emit := func(event string, detail any) error {
		logLock.Lock()
		defer logLock.Unlock()
		return encoder.Encode(struct {
			Event  string `json:"event"`
			Detail any    `json:"detail"`
		}{event, detail})
	}
	if err := emit("registered", map[string]any{"project": project.ID(), "epoch": epoch}); err != nil {
		return err
	}

	done := make(chan error, 2)
	go func() {
		done <- monitor.Run(ctx, epoch, func(result hostedquery.PollResult) error {
			if len(result.Windows) == 0 && len(result.Jobs) == 0 {
				return nil
			}
			var ids []int64
			decisions := map[hostedquery.Decision]int{}
			for _, window := range result.Windows {
				decisions[window.Decision]++
			}
			for _, job := range result.Jobs {
				ids = append(ids, job.ID)
			}
			return emit("poll", map[string]any{"decisions": decisions, "job_ids": ids,
				"target_ms": result.TargetMS, "total_ms": result.TotalMS})
		})
	}()
	go func() {
		done <- consumeProject(ctx, queue, target, project, epoch, queueInterval, investigate, emit)
	}()
	err := <-done
	cancel()
	<-done
	if errors.Is(err, context.Canceled) || errors.Is(err, hostedquery.ErrProjectDisconnected) {
		return nil
	}
	return monitorError("hosted project service", err)
}

func consumeProject(ctx context.Context, queue *pgx.Conn, target *pgx.ConnConfig, project supabaseproject.Project,
	epoch int64, interval time.Duration, investigate hostedquery.EnrolledLookupInvestigator,
	emit func(string, any) error) error {
	for ctx.Err() == nil {
		result, err := hostedquery.DiagnoseHostedProjectLookup(ctx, queue, target, project, epoch, investigate)
		if err != nil && ctx.Err() == nil {
			if queue.IsClosed() {
				return err
			}
			// A failed attempt retains the queue lease. Retry on the next due
			// check without blocking the monitor or printing driver details.
			if err := emit("diagnosis_attempt_failed", labdb.Diagnostic("diagnosis", err).Error()); err != nil {
				return err
			}
		}
		if result != nil {
			if err := emit("diagnosis_saved", savedDiagnosisEvent(result)); err != nil {
				return err
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	return ctx.Err()
}

// The detailed result stays in Pooltune state. Service logs carry only bounded
// operational fields, even if the model includes sensitive text in a diagnosis.
func savedDiagnosisEvent(result *hostedquery.DispatchResult) map[string]any {
	event := map[string]any{"job_id": result.JobID, "attempt": result.Attempt, "outcome": result.Outcome, "elapsed_ms": result.ElapsedMS}
	if result.LookupDiagnosis != nil {
		event["model_ms"] = result.LookupDiagnosis.ModelMS
		event["model"] = result.LookupDiagnosis.Model
		var assessments []struct {
			Cause      string `json:"cause"`
			Assessment string `json:"assessment"`
		}
		if json.Unmarshal(result.LookupDiagnosis.Assessments, &assessments) == nil {
			var supported []string
			for _, cause := range assessments {
				if cause.Assessment != "supported" {
					continue
				}
				switch cause.Cause {
				case "index", "analyze", "statistics", "vacuum", "memory", "cron", "unsupported":
					supported = append(supported, cause.Cause)
				}
			}
			event["supported_causes"] = supported
		}
	}
	return event
}
