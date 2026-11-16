//go:build integration

package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
)

func TestCommittedStatisticsPersistsReceiptBeforeCommitAndRestoresConfiguration(t *testing.T) {
	for _, variant := range []string{"column_target", "joint"} {
		for _, outcome := range []string{"improved_tested_inputs", "inconclusive", "regressed"} {
			t.Run(variant+"/"+outcome, func(t *testing.T) {
				ctx, config, _ := testpg.Start(t)
				db := openMonitorConn(t, ctx, config)
				intent := setupCommittedStatisticsTable(t, ctx, db, variant)
				guards, load := committedStatisticsTestJournal(t, ctx, db)
				called := false
				result, err := applyManagedStatisticsCommitted(ctx, config, intent, guards, func(ctx context.Context) (string, string, error) {
					called = true
					saved := load()
					if saved.Phase != "commit_ready" || saved.TrialVersion == "" || (variant == "joint" && saved.ObjectOID == 0) {
						t.Fatalf("committed native action outran durable identity: %+v", saved)
					}
					// This is a separate session, so the trial must really be committed.
					if variant == "column_target" {
						if setting := readProjectStatisticsSetting(t, ctx, db, intent.Target, 1); setting != (ProjectStatisticsSetting{Value: 2}) {
							t.Fatalf("uncommitted trial presented for verification: %+v", setting)
						}
					} else {
						var oid uint32
						if err := db.QueryRow(ctx, "SELECT oid FROM pg_statistic_ext WHERE stxname=$1", intent.ObjectName).Scan(&oid); err != nil || oid != saved.ObjectOID {
							t.Fatalf("joint trial not committed: %d %v", oid, err)
						}
					}
					return outcome, "verified test decision", nil
				})
				if err != nil || !called || !result.Committed || result.Outcome != outcome {
					t.Fatalf("committed trial: %+v %v", result, err)
				}
				restored := outcome != "improved_tested_inputs"
				if result.Restored != restored || result.Regenerated != restored {
					t.Fatalf("wrong restoration guarantee: %+v", result)
				}
				if variant == "column_target" {
					want := int32(2)
					if restored {
						want = 1
					}
					if setting := readProjectStatisticsSetting(t, ctx, db, intent.Target, 1); setting != (ProjectStatisticsSetting{Value: want}) {
						t.Fatalf("wrong final setting: %+v", setting)
					}
				} else {
					var count int
					if err := db.QueryRow(ctx, "SELECT count(*) FROM pg_statistic_ext WHERE stxrelid=$1", intent.Target.TableOID).Scan(&count); err != nil || (count == 0) != restored {
						t.Fatalf("wrong final object: %d %v", count, err)
					}
				}
			})
		}
	}
}

func TestCommittedStatisticsPreservesSameValueOwnerEdit(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	db := openMonitorConn(t, ctx, config)
	intent := setupCommittedStatisticsTable(t, ctx, db, "column_target")
	guards, _ := committedStatisticsTestJournal(t, ctx, db)
	result, err := applyManagedStatisticsCommitted(ctx, config, intent, guards, func(ctx context.Context) (string, string, error) {
		_, err := db.Exec(ctx, `ALTER TABLE customer_data.orders ALTER COLUMN account_id SET STATISTICS 2`)
		return "improved_tested_inputs", "candidate measured faster before owner edit", err
	})
	if err != nil || !result.OwnerChanged || result.Restored {
		t.Fatalf("same-value owner edit overwritten: %+v %v", result, err)
	}
	if setting := readProjectStatisticsSetting(t, ctx, db, intent.Target, 1); setting != (ProjectStatisticsSetting{Value: 2}) {
		t.Fatalf("owner setting lost: %+v", setting)
	}
}

func TestCommittedStatisticsRefusesRenamedOrReplacedJointObject(t *testing.T) {
	for _, edit := range []string{"rename", "replace"} {
		t.Run(edit, func(t *testing.T) {
			ctx, config, _ := testpg.Start(t)
			db := openMonitorConn(t, ctx, config)
			intent := setupCommittedStatisticsTable(t, ctx, db, "joint")
			guards, _ := committedStatisticsTestJournal(t, ctx, db)
			result, err := applyManagedStatisticsCommitted(ctx, config, intent, guards, func(ctx context.Context) (string, string, error) {
				sql := "ALTER STATISTICS " + qualified(intent.Target.Schema, intent.ObjectName) + " RENAME TO owner_statistics"
				if edit == "replace" {
					sql = "DROP STATISTICS " + qualified(intent.Target.Schema, intent.ObjectName) + ";CREATE STATISTICS " + qualified(intent.Target.Schema, intent.ObjectName) + " (dependencies) ON account_id,category FROM customer_data.orders"
				}
				_, err := db.Exec(ctx, sql)
				return "inconclusive", "owner edited object", err
			})
			if err != nil || !result.OwnerChanged || result.Restored {
				t.Fatalf("owner object changed: %+v %v", result, err)
			}
			var count int
			if err := db.QueryRow(ctx, "SELECT count(*) FROM pg_statistic_ext WHERE stxrelid=$1", intent.Target.TableOID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("owner object removed: %d %v", count, err)
			}
		})
	}
}

func TestCommittedStatisticsMissingEvidenceRestoresAndExpiredTrialCannotRetain(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	db := openMonitorConn(t, ctx, config)
	intent := setupCommittedStatisticsTable(t, ctx, db, "column_target")
	guards, _ := committedStatisticsTestJournal(t, ctx, db)
	result, err := applyManagedStatisticsCommitted(ctx, config, intent, guards, func(context.Context) (string, string, error) {
		return "", "", errors.New("no comparable application writes")
	})
	if err != nil || result.Outcome != "inconclusive" || !result.Restored || !result.Regenerated {
		t.Fatalf("missing traffic retained: %+v %v", result, err)
	}
	intent.Deadline = time.Now().Add(-time.Second)
	if _, err := db.Exec(ctx, `TRUNCATE trial_journal`); err != nil {
		t.Fatal(err)
	}
	result, err = applyManagedStatisticsCommitted(ctx, config, intent, guards, func(context.Context) (string, string, error) {
		t.Fatal("expired trial verified")
		return "improved_tested_inputs", "", nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || result.Committed {
		t.Fatalf("expired trial committed: %+v %v", result, err)
	}
}

func TestCommittedStatisticsDeadlineAfterCommitRestoresDespitePositiveVerifier(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	db := openMonitorConn(t, ctx, config)
	intent := setupCommittedStatisticsTable(t, ctx, db, "column_target")
	intent.Deadline = time.Now().Add(2 * time.Second)
	guards, _ := committedStatisticsTestJournal(t, ctx, db)
	result, err := applyManagedStatisticsCommitted(ctx, config, intent, guards, func(ctx context.Context) (string, string, error) {
		<-ctx.Done()
		return "improved_tested_inputs", "late result cannot authorize retention", nil
	})
	if err != nil || !result.Committed || result.Outcome != "inconclusive" || !result.Restored || !result.Regenerated {
		t.Fatalf("expired committed trial retained: %+v %v", result, err)
	}
}

func setupCommittedStatisticsTable(t *testing.T, ctx context.Context, db *pgx.Conn, variant string) ManagedStatisticsIntent {
	t.Helper()
	if _, err := db.Exec(ctx, `CREATE SCHEMA customer_data;CREATE TABLE customer_data.orders(account_id integer,category integer);INSERT INTO customer_data.orders SELECT i%100,i%10 FROM generate_series(1,10000)i;ALTER TABLE customer_data.orders ALTER COLUMN account_id SET STATISTICS 1;ANALYZE customer_data.orders`); err != nil {
		t.Fatal(err)
	}
	target := ProjectIndexTarget{Schema: "customer_data", Relation: "orders"}
	if err := db.QueryRow(ctx, `SELECT oid,'customer_data.orders'::regclass::oid FROM pg_database WHERE datname=current_database()`).Scan(&target.DatabaseOID, &target.TableOID); err != nil {
		t.Fatal(err)
	}
	intent := ManagedStatisticsIntent{Version: 1, RunID: "managed-statistics-42-1", Target: target, Keys: []ProjectIndexKey{{Number: 1, Name: "account_id"}}, Variant: variant, NextTarget: 2, Deadline: time.Now().Add(10 * time.Second)}
	if variant == "joint" {
		intent.Keys = append(intent.Keys, ProjectIndexKey{Number: 2, Name: "category"})
		intent.NextTarget = 0
		intent.ObjectName = managedJointStatisticsName(target.TableOID, []int16{1, 2})
	}
	return intent
}

func committedStatisticsTestJournal(t *testing.T, ctx context.Context, db *pgx.Conn) (managedStatisticsCommitGuards, func() ManagedStatisticsIntent) {
	t.Helper()
	if _, err := db.Exec(ctx, `CREATE TABLE trial_journal(intent jsonb NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	load := func() (intent ManagedStatisticsIntent) {
		t.Helper()
		var raw []byte
		if err := db.QueryRow(ctx, "SELECT intent FROM trial_journal").Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &intent); err != nil {
			t.Fatal(err)
		}
		return intent
	}
	guards := managedStatisticsCommitGuards{Permission: func(context.Context) error { return nil }, Capacity: func(context.Context) error { return nil }, Recheck: func(context.Context, pgx.Tx) error { return nil }, Record: func(ctx context.Context, intent ManagedStatisticsIntent) error {
		raw, _ := json.Marshal(intent)
		_, err := db.Exec(ctx, "INSERT INTO trial_journal VALUES($1::jsonb)", string(raw))
		return err
	}, Save: func(ctx context.Context, prior, next ManagedStatisticsIntent) error {
		a, _ := json.Marshal(prior)
		b, _ := json.Marshal(next)
		tag, err := db.Exec(ctx, "UPDATE trial_journal SET intent=$2::jsonb WHERE intent=$1::jsonb", string(a), string(b))
		if err == nil && tag.RowsAffected() != 1 {
			return errors.New("journal compare-and-set lost")
		}
		return err
	}}
	return guards, load
}

func TestCommittedStatisticsCannotCommitWithoutDurableReceipt(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	db := openMonitorConn(t, ctx, config)
	intent := setupCommittedStatisticsTable(t, ctx, db, "column_target")
	guards, load := committedStatisticsTestJournal(t, ctx, db)
	denied := errors.New("state receipt unavailable")
	guards.Save = func(context.Context, ManagedStatisticsIntent, ManagedStatisticsIntent) error { return denied }
	result, err := applyManagedStatisticsCommitted(ctx, config, intent, guards, func(context.Context) (string, string, error) {
		t.Fatal("unrecorded commit reached verification")
		return "", "", nil
	})
	if !errors.Is(err, denied) || result.Committed || load().Phase != "intent" {
		t.Fatalf("receipt failure committed: %+v %v", result, err)
	}
	if setting := readProjectStatisticsSetting(t, ctx, db, intent.Target, 1); setting != (ProjectStatisticsSetting{Value: 1}) {
		t.Fatalf("failed transaction leaked setting: %+v", setting)
	}
}

func TestCommittedStatisticsRestartRestoresWithoutReapplyingTrial(t *testing.T) {
	for _, variant := range []string{"column_target", "joint"} {
		t.Run(variant, func(t *testing.T) {
			ctx, config, _ := testpg.Start(t)
			db := openMonitorConn(t, ctx, config)
			intent := setupCommittedStatisticsTable(t, ctx, db, variant)
			guards, load := committedStatisticsTestJournal(t, ctx, db)
			actionConfig := config.Copy()
			actionConfig.RuntimeParams["application_name"] = "statistics-crash-boundary"
			result, err := applyManagedStatisticsCommitted(ctx, actionConfig, intent, guards, func(ctx context.Context) (string, string, error) {
				_, err := db.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name='statistics-crash-boundary'`)
				if err != nil {
					t.Fatal(err)
				}
				return "inconclusive", "worker lost after commit", errors.New("worker interrupted")
			})
			if err == nil || !result.Committed || load().Phase != "commit_ready" {
				t.Fatalf("expected durable uncertain commit: %+v %v", result, err)
			}
			restarted := openMonitorConn(t, ctx, config)
			if err := lockManagedStatistics(ctx, restarted, intent.Target.TableOID); err != nil {
				t.Fatal(err)
			}
			restored, err := restoreManagedStatisticsCommitted(ctx, restarted, load(), guards.Permission, guards.Save)
			if err != nil || !restored.Restored || !restored.Regenerated {
				t.Fatalf("restart did not restore: %+v %v", restored, err)
			}
			saved := load()
			if saved.Phase != "restore_ready" || saved.RestoreTransactionID == "" {
				t.Fatalf("restore receipt missing: %+v", saved)
			}
			// Reconciliation after a lost restoration acknowledgement must inspect the
			// committed receipt instead of executing another ALTER/ANALYZE.
			guards.Save = func(context.Context, ManagedStatisticsIntent, ManagedStatisticsIntent) error {
				t.Fatal("restoration repeated after acknowledgement loss")
				return nil
			}
			again, err := restoreManagedStatisticsCommitted(ctx, restarted, saved, guards.Permission, guards.Save)
			if err != nil || !again.Restored || !again.Regenerated {
				t.Fatalf("restoration acknowledgement not reconciled: %+v %v", again, err)
			}
		})
	}
}

func TestCommittedJointStatisticsRestoreLocksExactObjectAgainstReplacement(t *testing.T) {
	for _, edit := range []string{"rename", "drop"} {
		t.Run(edit, func(t *testing.T) {
			ctx, config, _ := testpg.Start(t)
			db := openMonitorConn(t, ctx, config)
			intent := setupCommittedStatisticsTable(t, ctx, db, "joint")
			guards, _ := committedStatisticsTestJournal(t, ctx, db)
			applied, err := applyManagedStatisticsCommitted(ctx, config, intent, guards, func(context.Context) (string, string, error) { return "improved_tested_inputs", "qualified", nil })
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(ctx, `CREATE TABLE customer_data.replacement(account_id integer,category integer)`); err != nil {
				t.Fatal(err)
			}
			actor := openMonitorConn(t, ctx, config)
			if _, err := actor.Exec(ctx, `SET lock_timeout='100ms'`); err != nil {
				t.Fatal(err)
			}
			restorer := openMonitorConn(t, ctx, config)
			if err := lockManagedStatistics(ctx, restorer, intent.Target.TableOID); err != nil {
				t.Fatal(err)
			}
			blocked := false
			restored, err := restoreManagedStatisticsCommitted(ctx, restorer, applied.Intent, func(ctx context.Context) error {
				statement := "ALTER STATISTICS " + qualified(intent.Target.Schema, intent.ObjectName) + " RENAME TO owner_saved"
				if edit == "drop" {
					statement = "DROP STATISTICS " + qualified(intent.Target.Schema, intent.ObjectName)
				}
				_, editErr := actor.Exec(ctx, statement)
				blocked = editErr != nil && strings.Contains(editErr.Error(), "lock timeout")
				if editErr == nil {
					_, editErr = actor.Exec(ctx, "CREATE STATISTICS "+qualified(intent.Target.Schema, intent.ObjectName)+" (mcv) ON account_id,category FROM customer_data.replacement")
					if editErr != nil {
						t.Fatal(editErr)
					}
				}
				return nil
			}, guards.Save)
			if err != nil || !restored.Restored || !blocked {
				t.Fatalf("owner replacement was not fenced: blocked=%v restored=%+v err=%v", blocked, restored, err)
			}
		})
	}
}
