package hostedquery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/jackc/pgx/v5"
)

type ConsumerFixture struct {
	ID         string     `json:"id"`
	Schema     string     `json:"schema"`
	Enrollment Enrollment `json:"enrollment"`
}

// CreateConsumerFixture creates a committed synthetic fixture so it can survive a worker restart.
func CreateConsumerFixture(ctx context.Context, target, state *pgx.Conn, route string) (ConsumerFixture, error) {
	return createConsumerFixture(ctx, target, state, route, false)
}

// CreateAnalyzeFixture prepares one private stale-statistics comparison.
func CreateAnalyzeFixture(ctx context.Context, target, state *pgx.Conn, route string) (ConsumerFixture, error) {
	return createConsumerFixture(ctx, target, state, route, true)
}

func createConsumerFixture(ctx context.Context, target, state *pgx.Conn, route string, analyze bool) (report ConsumerFixture, err error) {
	if err := prepareJournal(ctx, state); err != nil {
		return report, err
	}
	if err := lockTrial(ctx, target); err != nil {
		return report, err
	}
	defer func() { err = errors.Join(err, releaseTrialLock(target)) }()
	if err := trialCapacity(ctx, target); err != nil {
		return report, err
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return report, err
	}
	report.ID = hex.EncodeToString(nonce[:])
	report.Schema = trialSchema(report.ID)
	report.Enrollment.Project = route + "/consumer/" + report.ID
	if _, err := state.Exec(ctx, "INSERT INTO "+journalTable+" (id,target,status) VALUES($1,$2,'setup')", report.ID, journalTarget(target, route)); err != nil {
		return report, err
	}
	if err := createTrial(ctx, target, state, report.ID); err != nil {
		return report, err
	}
	status, source := "consumer_ready", ""
	if analyze {
		status, source = "analyze_ready", "ONLY "
		if err := writeTrialStatus(ctx, state, report.ID, "analyze_preparing", nil, nil); err != nil {
			return report, err
		}
		if err := seedStaleLookupStats(ctx, target, report.Schema); err != nil {
			return report, err
		}
	}
	err = pgx.BeginTxFunc(ctx, target, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var database, role uint32
		if err := tx.QueryRow(ctx, "SELECT oid,current_user::regrole::oid FROM pg_database WHERE datname=current_database()").Scan(&database, &role); err != nil {
			return err
		}
		for _, lookup := range []struct{ table, column string }{{"healthy", "id"}, {"candidate", "lookup"}} {
			sql := "SELECT id FROM " + source + qualified(report.Schema, lookup.table) + " WHERE " + lookup.column + "=$1"
			plan, err := readPlan(ctx, tx, "EXPLAIN (FORMAT JSON, VERBOSE) "+sql)
			if err != nil {
				return err
			}
			if plan.QueryID == 0 {
				return errors.New("fixture lookup has no statement ID")
			}
			report.Enrollment.Queries = append(report.Enrollment.Queries, QueryObjective{
				Database: database, Role: role, Query: plan.QueryID, MeanMS: objectiveMS,
			})
		}
		return nil
	})
	if err == nil {
		err = writeTrialStatus(ctx, state, report.ID, status, nil, nil)
	}
	return report, err
}

// RemoveConsumerFixture checks durable ownership and drops only the named test fixture.
func RemoveConsumerFixture(ctx context.Context, target, state *pgx.Conn, route, id string) (err error) {
	if err := lockTrial(ctx, target); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, releaseTrialLock(target)) }()
	var bound bool
	if err := state.QueryRow(ctx, "SELECT EXISTS (SELECT FROM "+journalTable+" WHERE id=$1 AND target=$2)",
		id, journalTarget(target, route)).Scan(&bound); err != nil || !bound {
		if err != nil {
			return err
		}
		return errors.New("fixture does not belong to this target")
	}
	status, err := cleanTrial(ctx, target, state, id)
	if err != nil {
		return err
	}
	return writeTrialStatus(ctx, state, id, "recovered", &status, nil)
}
