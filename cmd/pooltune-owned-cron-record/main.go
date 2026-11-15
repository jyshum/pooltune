// Command pooltune-owned-cron-record archives one newly observed period for the
// private hosted Rookery Cron fixture. It never launches traffic or changes jobs.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/hostedquery"
	"pooltune/internal/labdb"
)

func main() {
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "Pass one observed Cron JSON period on standard input.")
		os.Exit(2)
	}
	if err := record(os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func record(input io.Reader) error {
	if os.Getenv("POOLTUNE_STAGING_PRIVATE_STATE") != "1" {
		return errors.New("Cron recording requires the private Railway staging database")
	}
	fixture, err := hostedquery.ParseOwnedCronFixture(os.Getenv("POOLTUNE_OWNED_CRON_FIXTURE"),
		os.Getenv("POOLTUNE_OWNED_CRON_PASSWORD"), []byte(os.Getenv("POOLTUNE_OWNED_CRON_CA")))
	if err != nil {
		return err
	}
	period, err := hostedquery.DecodeOwnedCronPeriod(input)
	if err != nil {
		return err
	}
	config, err := labdb.StateConfig(os.Getenv("POOLTUNE_WORKER_STATE_DSN"))
	if err != nil {
		return errors.New("Cron recording requires the approved hosted worker state connection")
	}
	config.RuntimeParams["application_name"] = "pooltune-owned-cron-record"
	config.RuntimeParams["statement_timeout"] = "3s"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	state, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return labdb.Diagnostic("connect private Cron archive", err)
	}
	defer labdb.Close(state)
	if err := fixture.RecordObservedPeriod(ctx, state, period); err != nil {
		return labdb.Diagnostic("archive validated Cron period", err)
	}
	return nil
}
