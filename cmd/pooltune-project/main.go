// Command pooltune-project operates the private Supabase monitoring path.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"pooltune/internal/graphrunner"
	"pooltune/internal/hostedquery"
	"pooltune/internal/labdb"
	"pooltune/internal/supabaseproject"

	"github.com/jackc/pgx/v5"
)

const usage = "usage: pooltune-project {setup-sql|setup-evidence-sql|setup-plan-sql|connect|run|diagnose|serve|disconnect} -ref PROJECT_REF -key INSTALLATION_KEY"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := execute(ctx, os.Args[1:], os.Stdout); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func execute(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	command := args[0]
	switch command {
	case "setup-sql", "setup-evidence-sql", "setup-plan-sql", "connect", "run", "diagnose", "serve", "disconnect":
	default:
		return errors.New(usage)
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	ref := flags.String("ref", "", "Supabase project reference")
	key := flags.String("key", "", "16-character lowercase hex installation key")
	policy := hostedquery.SamplingPolicy{Interval: time.Minute, Window: 5 * time.Minute}
	queueInterval := 2 * time.Second
	if command == "run" || command == "serve" {
		flags.DurationVar(&policy.Interval, "interval", policy.Interval, "time between monitoring samples")
		flags.DurationVar(&policy.Window, "window", policy.Window, "time covered by one objective window")
	}
	if command == "serve" {
		flags.DurationVar(&queueInterval, "queue-interval", queueInterval, "time between diagnosis queue checks")
	}
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return errors.New(usage + " (run and serve accept -interval and -window)")
	}
	if queueInterval < 500*time.Millisecond || queueInterval > time.Minute {
		return errors.New("queue interval must be between 500ms and 1m")
	}
	project, err := supabaseproject.New(*ref, *key)
	if err != nil {
		return err
	}
	if command == "setup-sql" {
		_, err := io.WriteString(output, project.InstallSQL())
		return err
	}

	if command == "setup-evidence-sql" {
		_, err := io.WriteString(output, project.LookupEvidenceSQL())
		return err
	}
	if command == "setup-plan-sql" {
		_, err := io.WriteString(output, project.LookupPlanSQL())
		return err
	}

	stateDSN := os.Getenv("POOLTUNE_WORKER_STATE_DSN")
	stateConfig, err := labdb.StateConfig(stateDSN)
	if err != nil {
		return fmt.Errorf("Pooltune state configuration: %w", err)
	}
	stateConfig.RuntimeParams["application_name"] = "pooltune-project-state"
	stateConfig.RuntimeParams["statement_timeout"] = "5s"
	setup, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	state, err := pgx.ConnectConfig(setup, stateConfig)
	if err != nil {
		return labdb.Diagnostic("connect to Pooltune state", err)
	}
	defer labdb.Close(state)
	if err := hostedquery.PrepareWorkerState(setup, state); err != nil {
		return labdb.Diagnostic("prepare Pooltune state", err)
	}

	encoder := json.NewEncoder(output)
	if command == "disconnect" {
		if err := hostedquery.DisconnectProjectMonitor(setup, state, project); err != nil {
			return monitorError("disconnect project", err)
		}
		return encoder.Encode(map[string]string{"project": project.ID(), "status": "disconnected"})
	}
	var epoch int64
	if command == "run" || command == "diagnose" || command == "serve" {
		if command == "serve" {
			epoch, err = hostedquery.HostedProjectEpoch(setup, state, project)
		} else {
			epoch, err = hostedquery.ProjectMonitorEpoch(setup, state, project)
		}
		if command == "serve" && errors.Is(err, hostedquery.ErrProjectDisconnected) {
			return encoder.Encode(map[string]string{"event": "disconnected"})
		}
		if err != nil {
			return monitorError("read project registration", err)
		}
	}
	ca, err := os.ReadFile(os.Getenv("POOLTUNE_PROJECT_CA_FILE"))
	if err != nil {
		return errors.New("supply the Supabase CA with POOLTUNE_PROJECT_CA_FILE")
	}
	targetConfig, err := project.Config(os.Getenv("POOLTUNE_PROJECT_PASSWORD"), ca)
	if err != nil {
		return err
	}
	target, err := pgx.ConnectConfig(setup, targetConfig)
	if err != nil {
		return labdb.Diagnostic("connect to the Supabase monitor", err)
	}
	defer labdb.Close(target)
	if command == "diagnose" {
		cancel()
		investigate, err := graphrunner.ProjectLookupInvestigator(stateDSN)
		if err != nil {
			return err
		}
		result, err := hostedquery.DiagnoseProjectLookup(ctx, state, target, project, epoch, investigate)
		if err != nil {
			return monitorError("diagnose enrolled query", err)
		}
		return encoder.Encode(result)
	}
	monitor, err := hostedquery.NewProjectMonitor(target, state, project, policy)
	if err != nil {
		return err
	}
	if command == "serve" {
		investigate, err := graphrunner.ProjectLookupInvestigator(stateDSN)
		if err != nil {
			return err
		}
		queue, err := pgx.ConnectConfig(setup, stateConfig)
		if err != nil {
			return labdb.Diagnostic("connect diagnosis queue", err)
		}
		defer labdb.Close(queue)
		epoch, err = monitor.RegisterHosted(setup)
		if errors.Is(err, hostedquery.ErrProjectDisconnected) {
			return encoder.Encode(map[string]string{"event": "disconnected"})
		}
		if err != nil {
			return monitorError("register hosted monitor", err)
		}
		cancel()
		return serveProject(ctx, monitor, queue, targetConfig, project, epoch, queueInterval, investigate, encoder)
	}
	if command == "connect" {
		enrollment, err := monitor.Connect(setup)
		if err != nil {
			return monitorError("verify the Supabase monitor", err)
		}
		return encoder.Encode(enrollment)
	}
	cancel()
	err = monitor.Run(ctx, epoch, func(result hostedquery.PollResult) error {
		return encoder.Encode(result)
	})
	if errors.Is(err, hostedquery.ErrProjectDisconnected) {
		return nil
	}
	return monitorError("poll Supabase project", err)
}

func monitorError(stage string, err error) error {
	if err == nil || errors.Is(err, hostedquery.ErrProjectDisconnected) {
		return err
	}
	return labdb.Diagnostic(stage, err)
}
