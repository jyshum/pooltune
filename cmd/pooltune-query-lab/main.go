// Command pooltune-query-lab runs the bounded hosted-query M1 fixture.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"pooltune/internal/graphrunner"
	"pooltune/internal/hostedquery"
	"pooltune/internal/labdb"

	"github.com/jackc/pgx/v5"
)

func main() {
	route := flag.String("target", "local", "fixture route: local, rookery or staging")
	trial := flag.String("index-trial", "", "automatic index fixture: anthropic or scripted")
	pauseAfterBuild := flag.Bool("pause-after-index-build", false, "pause after a synthetic index build for the Railway recovery drill")
	recoverOnly := flag.Bool("recover-index-trials", false, "reconcile interrupted fixture runs")
	trafficTrial := flag.Bool("worker-traffic", false, "observe a bounded read-only traffic stream from another session")
	investigation := flag.String("worker-investigation", "", "investigate the queued traffic job: anthropic or scripted")
	vacuumFixture := flag.Bool("vacuum-fixture", false, "seed an indexed visibility fixture for bounded plain VACUUM")
	statisticsFixture := flag.Bool("statistics-fixture", false, "seed correlated lookup columns for a staging fixture")
	analyzeFixture := flag.Bool("analyze-fixture", false, "seed stale lookup statistics when creating a staging fixture")
	createConsumer := flag.Bool("create-consumer-fixture", false, "create a journaled synthetic fixture for the continuous consumer")
	removeConsumer := flag.String("remove-consumer-fixture", "", "remove a journaled synthetic fixture by ID")
	flag.Parse()
	if (*createConsumer || *removeConsumer != "") && (*trafficTrial || *trial != "" || *recoverOnly || (*createConsumer && *removeConsumer != "")) {
		flag.Usage()
		os.Exit(2)
	}
	if (*trial != "" && *trial != "anthropic" && *trial != "scripted") || (*recoverOnly && *trial != "") || (*trafficTrial && (*recoverOnly || *trial != "")) {
		fmt.Fprintln(os.Stderr, "choose an explicit index trial mode or recovery")
		os.Exit(2)
	}
	if *investigation != "" && (!*trafficTrial || (*investigation != "scripted" && *investigation != "anthropic")) {
		fmt.Fprintln(os.Stderr, "choose scripted or anthropic worker investigation with -worker-traffic")
		os.Exit(2)
	}
	if (*analyzeFixture || *statisticsFixture || *vacuumFixture) && (!*createConsumer || *route != "rookery" || os.Getenv("POOLTUNE_STAGING_PRIVATE_STATE") != "1" || (*analyzeFixture && *statisticsFixture) || (*vacuumFixture && (*analyzeFixture || *statisticsFixture))) {
		fmt.Fprintln(os.Stderr, "statistics fixtures require Rookery, Railway private state, and one selected fixture")
		os.Exit(2)
	}
	if *pauseAfterBuild && (*route != "rookery" || *trial != "scripted" || os.Getenv("POOLTUNE_STAGING_PRIVATE_STATE") != "1") {
		fmt.Fprintln(os.Stderr, "recovery drill requires the scripted Rookery trial and Railway private state")
		os.Exit(2)
	}
	if *route == "staging" && !*createConsumer && *removeConsumer == "" {
		fmt.Fprintln(os.Stderr, "staging supports synthetic fixture setup and cleanup")
		os.Exit(2)
	}
	if flag.NArg() != 0 || (*route != "local" && *route != "rookery" && *route != "staging") {
		fmt.Fprintln(os.Stderr, "select -target local, rookery or staging and supply connection settings in the environment")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
	err := run(ctx, *route, *trial, *recoverOnly, *trafficTrial, *investigation, *createConsumer, *removeConsumer, *pauseAfterBuild, *analyzeFixture, *statisticsFixture, *vacuumFixture)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, route string, trial string, recoverOnly, trafficTrial bool, investigation string, createConsumer bool, removeConsumer string, pauseAfterBuild, analyzeFixture, statisticsFixture, vacuumFixture bool) error {
	stateConfig, err := labdb.StateConfig(os.Getenv("POOLTUNE_LAB_STATE_DSN"))
	if err != nil {
		return fmt.Errorf("state configuration: %w", err)
	}
	targetConfig, err := labdb.TargetConfig(os.Getenv("POOLTUNE_LAB_TARGET_DSN"), route)
	if err != nil {
		return fmt.Errorf("target configuration: %w", err)
	}
	if stateConfig.Host == targetConfig.Host && stateConfig.Port == targetConfig.Port &&
		stateConfig.Database == targetConfig.Database {
		return errors.New("state and target must use separate databases")
	}
	var investigator hostedquery.IndexInvestigator
	if mode := trial + investigation; mode != "" {
		investigator, err = graphrunner.Process(mode, os.Getenv("POOLTUNE_LAB_STATE_DSN"))
		if err != nil {
			return err
		}
	}
	state, err := pgx.ConnectConfig(ctx, stateConfig)
	if err != nil {
		return labdb.Diagnostic("connect Pooltune state", err)
	}
	defer labdb.Close(state)
	if err := hostedquery.PrepareState(ctx, state); err != nil {
		return labdb.Diagnostic("prepare Pooltune state", err)
	}
	target, err := pgx.ConnectConfig(ctx, targetConfig)
	if err != nil {
		return labdb.Diagnostic("connect fixture target", err)
	}
	defer labdb.Close(target)

	identity := route
	if createConsumer {
		create := hostedquery.CreateConsumerFixture
		if vacuumFixture {
			create = hostedquery.CreateVacuumFixture
		} else if analyzeFixture {
			create = hostedquery.CreateAnalyzeFixture
		} else if statisticsFixture {
			create = hostedquery.CreateStatisticsFixture
		}
		report, err := create(ctx, target, state, identity)
		if err != nil {
			if report.ID != "" {
				fmt.Fprintf(os.Stderr, "Fixture %s can be reconciled against the same databases.\n", report.ID)
			}
			return labdb.Diagnostic("create consumer fixture", err)
		}
		return json.NewEncoder(os.Stdout).Encode(report)
	}
	if removeConsumer != "" {
		if err := hostedquery.RemoveConsumerFixture(ctx, target, state, identity, removeConsumer); err != nil {
			return labdb.Diagnostic("remove consumer fixture", err)
		}
		return nil
	}
	if trafficTrial {
		observerConfig := targetConfig.Copy()
		observerConfig.RuntimeParams["application_name"] = "pooltune-traffic-observer"
		observerConfig.RuntimeParams["default_transaction_read_only"] = "on"
		observerConfig.RuntimeParams["statement_timeout"] = "2s"
		observer, err := pgx.ConnectConfig(ctx, observerConfig)
		if err != nil {
			return labdb.Diagnostic("connect traffic observer", err)
		}
		defer labdb.Close(observer)
		report, err := hostedquery.RunWorkerTraffic(ctx, target, observer, state, identity, investigator)
		if err != nil {
			return labdb.Diagnostic("run worker traffic", err)
		}
		return json.NewEncoder(os.Stdout).Encode(report)
	}
	if recoverOnly {
		if err := hostedquery.RecoverIndexTrials(ctx, target, state, identity); err != nil {
			return labdb.Diagnostic("recover fixture", err)
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"status": "reconciled"})
	}
	if trial != "" {
		var report hostedquery.IndexTrialReport
		if pauseAfterBuild {
			report, err = hostedquery.DrillIndexTrialRecovery(ctx, target, state, investigator, identity, func() {
				fmt.Fprintln(os.Stderr, "Recovery drill: index build returned; waiting for process stop.")
			})
		} else {
			report, err = hostedquery.RunIndexTrial(ctx, target, state, investigator, identity)
		}
		if err != nil {
			if report.RunID != "" {
				fmt.Fprintf(os.Stderr, "Index trial %s has a journal record. Use -recover-index-trials on the same target if pending.\n", report.RunID)
			}
			return labdb.Diagnostic("run index trial", err)
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			Investigator string                       `json:"investigator"`
			Report       hostedquery.IndexTrialReport `json:"report"`
		}{Investigator: trial, Report: report})
	}
	report, err := hostedquery.RunLab(ctx, target)
	if err != nil {
		return labdb.Diagnostic("run fixture", err)
	}
	id, err := hostedquery.RecordReport(ctx, state, report)
	if err != nil {
		return labdb.Diagnostic("store report", err)
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		ID     int64              `json:"id"`
		Report hostedquery.Report `json:"report"`
	}{ID: id, Report: report})
}
