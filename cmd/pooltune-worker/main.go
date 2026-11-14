// Command pooltune-worker observes enrolled queries and queues investigations.
package main

import (
	"bytes"
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

	"pooltune/internal/hostedquery"
	"pooltune/internal/labdb"

	"github.com/jackc/pgx/v5"
)

const enrollmentLimit = 32 << 10

func main() {
	route := flag.String("target", "local", "development target: local, rookery, staging or rookery-enrolled")
	enrollmentPath := flag.String("enrollment", "", "path to query enrollment JSON")
	interval := flag.Duration("interval", time.Minute, "time between sampling ticks")
	window := flag.Duration("window", 5*time.Minute, "duration of one objective window")
	flag.Parse()
	if flag.NArg() != 0 || (*route != "local" && *route != "rookery" && *route != "staging" && *route != "rookery-enrolled") || *enrollmentPath == "" {
		flag.Usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := run(ctx, *route, *enrollmentPath, hostedquery.SamplingPolicy{Interval: *interval, Window: *window})
	if err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func readEnrollment(input io.Reader) (hostedquery.Enrollment, error) {
	var enrollment hostedquery.Enrollment
	data, err := io.ReadAll(io.LimitReader(input, enrollmentLimit+1))
	if err != nil || len(data) > enrollmentLimit {
		return enrollment, errors.New("cannot read enrollment within 32 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&enrollment); err != nil || !json.Valid(data) {
		return enrollment, errors.New("invalid enrollment JSON")
	}
	return enrollment, nil
}

func run(ctx context.Context, route string, enrollmentPath string, policy hostedquery.SamplingPolicy) error {
	file, err := os.Open(enrollmentPath)
	if err != nil {
		return errors.New("cannot open enrollment file")
	}
	enrollment, err := readEnrollment(file)
	file.Close()
	if err != nil {
		return err
	}
	targetConfig, err := labdb.TargetConfig(os.Getenv("POOLTUNE_WORKER_TARGET_DSN"), route)
	if err != nil {
		return fmt.Errorf("worker target configuration: %w", err)
	}
	stateConfig, err := labdb.StateConfig(os.Getenv("POOLTUNE_WORKER_STATE_DSN"))
	if err != nil {
		return fmt.Errorf("worker state configuration: %w", err)
	}
	if targetConfig.Host == stateConfig.Host && targetConfig.Port == stateConfig.Port && targetConfig.Database == stateConfig.Database {
		return errors.New("worker target and state must use separate databases")
	}
	stateConfig.RuntimeParams["application_name"] = "pooltune-worker-state"
	targetConfig.RuntimeParams["application_name"] = "pooltune-worker"
	targetConfig.RuntimeParams["default_transaction_read_only"] = "on"
	targetConfig.RuntimeParams["statement_timeout"] = "2s"
	setup, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	target, err := pgx.ConnectConfig(setup, targetConfig)
	if err != nil {
		return labdb.Diagnostic("connect worker target", err)
	}
	defer labdb.Close(target)
	if route == "rookery-enrolled" {
		if err := verifyDedicatedDetector(setup, target, enrollment); err != nil {
			return labdb.Diagnostic("verify dedicated detector identity", err)
		}
	}
	state, err := pgx.ConnectConfig(setup, stateConfig)
	if err != nil {
		return labdb.Diagnostic("connect worker state", err)
	}
	defer labdb.Close(state)
	worker, err := hostedquery.NewWorker(setup, target, state, enrollment, policy)
	if err != nil {
		return labdb.Diagnostic("configure worker", err)
	}
	if err := hostedquery.PrepareWorkerState(setup, state); err != nil {
		return labdb.Diagnostic("prepare worker state", err)
	}
	cancel()
	encoder := json.NewEncoder(os.Stdout)
	if err := worker.Run(ctx, func(result hostedquery.PollResult) error { return encoder.Encode(result) }); err != nil {
		return labdb.Diagnostic("poll worker", err)
	}
	return nil
}

// The first enrolled Rookery path uses the same narrow identity for traffic and
// statistics. Cross-role collection needs its own access decision.
func verifyDedicatedDetector(ctx context.Context, target *pgx.Conn, enrollment hostedquery.Enrollment) error {
	var role, session uint32
	if err := target.QueryRow(ctx, "SELECT current_user::regrole::oid, session_user::regrole::oid").Scan(&role, &session); err != nil {
		return err
	}
	if role != session {
		return errors.New("worker must use its authenticated role")
	}
	for _, query := range enrollment.Queries {
		if query.Role != role {
			return errors.New("worker can observe only statements from its authenticated role")
		}
	}
	return nil
}
