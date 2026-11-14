// Command pooltune-consumer continuously processes one enrolled project's investigations.
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

	"pooltune/internal/graphrunner"
	"pooltune/internal/hostedquery"
	"pooltune/internal/labdb"

	"github.com/jackc/pgx/v5"
)

func main() {
	route := flag.String("target", "local", "development target: local, rookery or staging")
	project := flag.String("project", "", "enrolled project identity")
	cron := flag.Bool("cron-staging-fixture", false, "qualify enrolled future Cron schedule")
	memory := flag.Bool("memory-staging-fixture", false, "qualify enrolled function memory from the staging queue")
	vacuum := flag.Bool("vacuum-staging-fixture", false, "qualify bounded plain VACUUM from the staging queue")
	statistics := flag.Bool("statistics-staging-fixture", false, "qualify richer planner statistics from the staging queue")
	analyze := flag.Bool("analyze-staging-fixture", false, "qualify one targeted ANALYZE from the staging queue")
	repair := flag.Bool("repair-staging-fixture", false, "qualify one owned Rookery fixture repair from the staging queue")
	measure := flag.Bool("measure-staging-lookup", false, "measure one authorized synthetic Rookery lookup")
	enrolledPath := flag.String("enrolled-lookup", "", "explicit Rookery evidence enrollment JSON")
	fixture := flag.String("fixture", "", "journaled synthetic fixture ID")
	mode := flag.String("mode", "scripted", "investigator: scripted or anthropic")
	poll := flag.Duration("poll", 2*time.Second, "wait between empty queue checks")
	flag.Parse()
	if flag.NArg() != 0 || (*route != "local" && *route != "rookery" && *route != "staging" && *route != "rookery-enrolled") ||
		(*mode != "scripted" && *mode != "anthropic") || *project == "" || len(*project) > 128 ||
		*poll < 100*time.Millisecond || *poll > time.Minute {
		flag.Usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if (*route == "staging" || *route == "rookery-enrolled") && *mode != "scripted" {
		fmt.Fprintln(os.Stderr, "staging uses the scripted investigator")
		os.Exit(2)
	}
	if err := run(ctx, *route, *project, *fixture, *mode, *enrolledPath, *measure, *repair, *analyze, *statistics, *vacuum, *memory, *poll, *cron); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, route, project, fixture, mode, enrolledPath string, measure, repair, analyze, statistics, vacuum, memory bool, poll time.Duration, cronOption ...bool) error {
	cron := len(cronOption) > 0 && cronOption[0]
	target, err := labdb.TargetConfig(os.Getenv("POOLTUNE_WORKER_TARGET_DSN"), route)
	if err != nil {
		return fmt.Errorf("consumer target: %w", err)
	}
	stateDSN := os.Getenv("POOLTUNE_WORKER_STATE_DSN")
	stateConfig, err := labdb.StateConfig(stateDSN)
	if err != nil {
		return fmt.Errorf("consumer state: %w", err)
	}
	if target.Host == stateConfig.Host && target.Port == stateConfig.Port && target.Database == stateConfig.Database {
		return errors.New("consumer target and state must use separate databases")
	}
	target.RuntimeParams["application_name"] = "pooltune-consumer"
	if !repair && !analyze && !statistics && !vacuum && !memory && !cron {
		target.RuntimeParams["default_transaction_read_only"] = "on"
	}
	stateConfig.RuntimeParams["application_name"] = "pooltune-consumer-state"
	stateConfig.RuntimeParams["statement_timeout"] = "3s"
	if (repair || analyze || statistics || vacuum || memory || cron) && (route != "rookery" || (mode != "scripted" && mode != "anthropic") || os.Getenv("POOLTUNE_STAGING_PRIVATE_STATE") != "1" || measure || (repair && analyze) || (statistics && (repair || analyze)) || (vacuum && (repair || analyze || statistics)) || (memory && (repair || analyze || statistics || vacuum)) || (cron && (repair || analyze || statistics || vacuum || memory))) {
		return errors.New("repair requires the enrolled Rookery fixture and Railway private state")
	}
	if measure && (route != "rookery-enrolled" || mode != "scripted") {
		return errors.New("measurement requires the dedicated Rookery staging route and scripted investigator")
	}
	var investigate hostedquery.IndexInvestigator
	var enrollment hostedquery.LookupEnrollment
	if route == "rookery-enrolled" {
		if fixture != "" {
			return errors.New("choose one evidence source")
		}
		var err error
		enrollment, err = readLookupEnrollment(enrolledPath)
		if err != nil {
			return err
		}
		if project != enrollment.Project {
			return errors.New("project does not match enrolled lookup")
		}
	} else {
		if enrolledPath != "" {
			return errors.New("enrolled lookup requires the dedicated Rookery route")
		}
		if err := hostedquery.ValidateConsumerFixtureID(fixture); err != nil {
			return err
		}
		if project != route+"/consumer/"+fixture {
			return errors.New("project does not match the enrolled consumer fixture")
		}
	}
	if route != "rookery-enrolled" || measure {
		investigate, err = graphrunner.Process(mode, stateDSN)
		if err != nil {
			return err
		}
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
			if err == nil {
				fmt.Fprintln(os.Stderr, "consumer connected to Pooltune state")
			}
		} else {
			err = nil
		}
		if err == nil {
			var result *hostedquery.DispatchResult
			var workErr error
			if repair || analyze || statistics || vacuum || memory || cron {
				binding := hostedquery.ConsumerFixture{ID: fixture, Enrollment: hostedquery.Enrollment{Project: project}}
				dispatch := hostedquery.DispatchFixtureRepair
				if cron {
					dispatch = hostedquery.DispatchFixtureCron
				} else if memory {
					dispatch = hostedquery.DispatchFixtureMemory
				} else if vacuum {
					dispatch = hostedquery.DispatchFixtureVacuum
				} else if analyze {
					dispatch = hostedquery.DispatchFixtureAnalyze
				} else if statistics {
					dispatch = hostedquery.DispatchFixtureStatistics
				}
				result, workErr = dispatch(ctx, state, target, route, binding, investigate)
			} else if measure {
				result, workErr = hostedquery.DispatchMeasuredStagingLookup(ctx, state, target, enrollment, investigate)
			} else if route == "rookery-enrolled" {
				result, workErr = hostedquery.DispatchEstimatedInvestigation(ctx, state, project, hostedquery.EnrolledEstimate(target, enrollment))
			} else {
				collect := hostedquery.FixtureEvidence(state, target, route, fixture)
				result, workErr = hostedquery.DispatchInvestigation(ctx, state, project, collect, investigate)
			}
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

func readLookupEnrollment(path string) (hostedquery.LookupEnrollment, error) {
	var enrollment hostedquery.LookupEnrollment
	file, err := os.Open(path)
	if err != nil {
		return enrollment, errors.New("cannot open enrolled lookup file")
	}
	defer file.Close()
	const limit = 16 << 10
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(data) > limit {
		return enrollment, errors.New("cannot read enrolled lookup within 16 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&enrollment); err != nil || !json.Valid(data) {
		return enrollment, errors.New("invalid enrolled lookup JSON")
	}
	return enrollment, enrollment.Validate()
}
