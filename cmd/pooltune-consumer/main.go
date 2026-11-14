// Command pooltune-consumer continuously processes one enrolled project's investigations.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"pooltune/internal/graphrunner"
	"pooltune/internal/hostedquery"
	"pooltune/internal/labdb"

	"github.com/jackc/pgx/v5"
)

func main() {
	route := flag.String("target", "local", "development target: local or rookery")
	project := flag.String("project", "", "enrolled project identity")
	fixture := flag.String("fixture", "", "journaled synthetic fixture ID")
	mode := flag.String("mode", "scripted", "investigator: scripted or anthropic")
	poll := flag.Duration("poll", 2*time.Second, "wait between empty queue checks")
	flag.Parse()
	if flag.NArg() != 0 || (*route != "local" && *route != "rookery") ||
		(*mode != "scripted" && *mode != "anthropic") || *project == "" || len(*project) > 128 ||
		*poll < 100*time.Millisecond || *poll > time.Minute {
		flag.Usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *route, *project, *fixture, *mode, *poll); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, route, project, fixture, mode string, poll time.Duration) error {
	target, err := labdb.Config(os.Getenv("POOLTUNE_WORKER_TARGET_DSN"), route == "rookery")
	if err != nil {
		return fmt.Errorf("consumer target: %w", err)
	}
	stateDSN := os.Getenv("POOLTUNE_WORKER_STATE_DSN")
	stateConfig, err := labdb.Config(stateDSN, false)
	if err != nil {
		return fmt.Errorf("consumer state: %w", err)
	}
	if target.Host == stateConfig.Host && target.Port == stateConfig.Port && target.Database == stateConfig.Database {
		return errors.New("consumer target and state must use separate databases")
	}
	target.RuntimeParams["application_name"] = "pooltune-consumer"
	target.RuntimeParams["default_transaction_read_only"] = "on"
	stateConfig.RuntimeParams["application_name"] = "pooltune-consumer-state"
	stateConfig.RuntimeParams["statement_timeout"] = "3s"
	investigate, err := graphrunner.Process(mode, stateDSN)
	if err != nil {
		return err
	}
	// Validate the fixture argument before entering the reconnect loop.
	if err := hostedquery.ValidateConsumerFixtureID(fixture); err != nil {
		return err
	}
	if project != route+"/consumer/"+fixture {
		return errors.New("project does not match the enrolled consumer fixture")
	}
	encoder := json.NewEncoder(os.Stdout)
	var state *pgx.Conn
	defer func() {
		if state != nil {
			labdb.Close(state)
		}
	}()
	delay := poll
	for ctx.Err() == nil {
		if state == nil {
			setup, cancel := context.WithTimeout(ctx, 5*time.Second)
			state, err = connectState(setup, stateConfig)
			cancel()
		} else {
			err = nil
		}
		if err == nil {
			collect := hostedquery.FixtureEvidence(state, target, route, fixture)
			result, workErr := hostedquery.DispatchInvestigation(ctx, state, project, collect, investigate)
			if result != nil && workErr == nil {
				if err := encoder.Encode(result); err != nil {
					return err
				}
				delay = poll
				continue
			}
			err = workErr
		}
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, labdb.Diagnostic("consumer attempt", err))
			if state != nil && state.IsClosed() {
				state = nil
			}
			delay = max(poll, min(30*time.Second, delay*2))
		} else {
			delay = poll
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	return nil
}

func connectState(ctx context.Context, config *pgx.ConnConfig) (*pgx.Conn, error) {
	state, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := hostedquery.PrepareWorkerState(ctx, state); err != nil {
		labdb.Close(state)
		return nil, err
	}
	return state, nil
}
