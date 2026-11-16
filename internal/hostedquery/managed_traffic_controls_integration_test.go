//go:build integration

package hostedquery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
)

func TestManagedTrafficControlsRequireLiveWritesAndUnchangedCounters(t *testing.T) {
	ctx, config, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	db, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	if _, err := db.Exec(ctx, `CREATE EXTENSION pg_stat_statements;CREATE TABLE public.traffic_control(id int PRIMARY KEY,n int);INSERT INTO public.traffic_control VALUES(1,0);GRANT SELECT,UPDATE ON public.traffic_control TO fixture_app`); err != nil {
		t.Fatal(err)
	}
	var database, role uint32
	if err := db.QueryRow(ctx, `SELECT oid,'fixture_app'::regrole::oid FROM pg_database WHERE datname=current_database()`).Scan(&database, &role); err != nil {
		t.Fatal(err)
	}
	if _, err := observeManagedControls(ctx, db, database, role, 1, nil); err == nil {
		t.Fatal("reported protection without real traffic")
	}
	appConfig := config.Copy()
	appConfig.User = "fixture_app"
	app, err := pgx.ConnectConfig(ctx, appConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(ctx)
	trafficCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		for {
			if trafficCtx.Err() != nil {
				done <- nil
				return
			}
			if _, err := app.Exec(trafficCtx, `SELECT n,pg_sleep(0.002) FROM public.traffic_control WHERE id=$1`, 1); err != nil {
				done <- err
				return
			}
			if _, err := app.Exec(trafficCtx, `UPDATE public.traffic_control SET n=n+1 WHERE id=$1`, 1); err != nil {
				done <- err
				return
			}
		}
	}()
	defer func() { stop(); <-done }()
	// Ensure both statement identities are visible before the measured interval.
	for deadline := time.Now().Add(time.Second); ; {
		var count int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM pg_stat_statements WHERE userid=$1 AND query~'^(SELECT|UPDATE)'`, role).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("traffic did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	baseline, err := observeManagedControls(ctx, db, database, role, 1, nil)
	if err != nil || baseline.ReadCalls == 0 || baseline.WriteCalls == 0 {
		t.Fatalf("missing live traffic: %+v %v", baseline, err)
	}
	for key := range baseline.means {
		baseline.means[key] = 1e-12
	}
	regressed, err := observeManagedControls(ctx, db, database, role, 1, baseline)
	if !errors.Is(err, ErrManagedTrafficRegression) || regressed == nil || regressed.ReadCalls == 0 || regressed.WriteCalls == 0 {
		t.Fatalf("known regression lost measured proof: %+v %v", regressed, err)
	}
	if _, err := db.Exec(ctx, `SELECT pg_stat_statements_reset()`); err != nil {
		t.Fatal(err)
	}
	if _, err := observeManagedControls(ctx, db, database, role, 1, baseline); err == nil {
		t.Fatal("compared protection across a counter reset")
	}
	var writes int
	if err := db.QueryRow(ctx, `SELECT n FROM public.traffic_control`).Scan(&writes); err != nil || writes <= 0 {
		t.Fatalf("actual app writes were absent: %d %v", writes, err)
	}
}
