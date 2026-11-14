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
	route := flag.String("target", "local", "fixture route: local or rookery")
	trial := flag.String("index-trial", "", "automatic index fixture: anthropic or scripted")
	recoverOnly := flag.Bool("recover-index-trials", false, "reconcile interrupted fixture runs")
	trafficTrial := flag.Bool("worker-traffic", false, "observe a bounded read-only traffic stream from another session")
	investigation := flag.String("worker-investigation", "", "investigate the queued traffic job: anthropic or scripted")
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
	if flag.NArg() != 0 || (*route != "local" && *route != "rookery") {
		fmt.Fprintln(os.Stderr, "select -target local or -target rookery and supply connection settings in the environment")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
	err := run(ctx, *route == "rookery", *trial, *recoverOnly, *trafficTrial, *investigation, *createConsumer, *removeConsumer)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, rookery bool, trial string, recoverOnly, trafficTrial bool, investigation string, createConsumer bool, removeConsumer string) error {
	stateConfig, err := labdb.Config(os.Getenv("POOLTUNE_LAB_STATE_DSN"), false)
	if err != nil {
		return fmt.Errorf("state configuration: %w", err)
	}
	targetConfig, err := labdb.Config(os.Getenv("POOLTUNE_LAB_TARGET_DSN"), rookery)
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

	identity := "local"
	if rookery {
		identity = "rookery"
	}
	if createConsumer {
		report, err := hostedquery.CreateConsumerFixture(ctx, target, state, identity)
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
		report, err := hostedquery.RunIndexTrial(ctx, target, state, investigator, identity)
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
