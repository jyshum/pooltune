//go:build integration

package hostedquery

import (
	"pooltune/internal/testpg"
	"strings"
	"testing"
	"time"
)

func TestManagedCronStoreFencesObservationsButRetainsExactCleanupAcrossEpochs(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t)
	db := openMonitorConn(t, ctx, cfg)
	if err := PrepareWorkerState(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := PrepareManagedCronState(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO pooltune_worker.projects(project_id,monitor_epoch)VALUES('cron-project',1),('other-project',1)`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	observation := ManagedCronObservation{At: now}
	if err := SaveManagedCronObservation(ctx, db, "cron-project", 1, 17, time.Hour, observation); err != nil {
		t.Fatal(err)
	}
	witness := ManagedCronWitness{Project: "cron-project", Name: "pooltune_witness_exact", Fingerprint: strings.Repeat("a", 64), Generation: "100", DatabaseOID: 1, OwnerOID: 2, JobID: 18, Expires: now.Add(time.Minute)}
	if err := SaveManagedCronWitness(ctx, db, "cron-project", 1, witness); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE pooltune_worker.projects SET monitor_epoch=2 WHERE project_id='cron-project'`); err != nil {
		t.Fatal(err)
	}
	if err := SaveManagedCronObservation(ctx, db, "cron-project", 1, 17, time.Hour, observation); err == nil {
		t.Fatal("stale worker wrote observation")
	}
	if err := SaveManagedCronWitness(ctx, db, "cron-project", 1, witness); err == nil {
		t.Fatal("stale worker created witness identity")
	}
	old, err := ReadManagedCronObservations(ctx, db, "cron-project", 1, 17, now.Add(-time.Hour))
	if err != nil || len(old) != 0 {
		t.Fatal("old epoch evidence remained admissible", err)
	}
	pending, err := PendingManagedCronWitnesses(ctx, db, "cron-project")
	if err != nil || len(pending) != 1 {
		t.Fatal("cleanup identity disappeared with epoch", err)
	}
	foreign, err := PendingManagedCronWitnesses(ctx, db, "other-project")
	if err != nil || len(foreign) != 0 {
		t.Fatal("another project received cleanup identity", err)
	}
	changed := pending[0]
	changed.JobID++
	if err := SettleManagedCronWitness(ctx, db, changed); err == nil {
		t.Fatal("different cleanup identity settled")
	}
	if err := SaveManagedCronWitnessFence(ctx, db, pending[0], "101"); err != nil {
		t.Fatal(err)
	}
	if err := SaveManagedCronWitnessFence(ctx, db, pending[0], "102"); err == nil {
		t.Fatal("different native fence overwrote cleanup identity")
	}
	pending, err = PendingManagedCronWitnesses(ctx, db, "cron-project")
	if err != nil || len(pending) != 1 || pending[0].Generation != "100" || pending[0].FenceGeneration != "101" {
		t.Fatal("known abort/commit identities not retained", err)
	}
	if err := SettleManagedCronWitness(ctx, db, pending[0]); err != nil {
		t.Fatal(err)
	}
	pending, err = PendingManagedCronWitnesses(ctx, db, "cron-project")
	if err != nil || len(pending) != 0 {
		t.Fatal("settled cleanup remained pending", err)
	}
}
