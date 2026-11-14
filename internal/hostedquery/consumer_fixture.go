package hostedquery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type ConsumerFixture struct {
	ID                string     `json:"id"`
	Schema            string     `json:"schema"`
	OrdinaryAnalyzeMS float64    `json:"ordinary_analyze_wall_ms,omitempty"`
	Enrollment        Enrollment `json:"enrollment"`
}

// CreateConsumerFixture creates a committed synthetic fixture so it can survive a worker restart.
func CreateConsumerFixture(ctx context.Context, target, state *pgx.Conn, route string) (ConsumerFixture, error) {
	return createConsumerFixture(ctx, target, state, route, "")
}

// CreateAnalyzeFixture prepares one private stale-statistics comparison.
func CreateAnalyzeFixture(ctx context.Context, target, state *pgx.Conn, route string) (ConsumerFixture, error) {
	return createConsumerFixture(ctx, target, state, route, "analyze")
}

// CreateStatisticsFixture prepares correlated columns with current single-column statistics.
func CreateStatisticsFixture(ctx context.Context, target, state *pgx.Conn, route string) (ConsumerFixture, error) {
	return createConsumerFixture(ctx, target, state, route, "statistics")
}

// CreateVacuumFixture prepares a healthy indexed aggregate for visibility-loss tests.
func CreateVacuumFixture(ctx context.Context, target, state *pgx.Conn, route string) (ConsumerFixture, error) {
	if err := recoverVacuumPreparations(ctx, target, state, route); err != nil {
		return ConsumerFixture{}, err
	}
	fixture, err := createConsumerFixture(ctx, target, state, route, "vacuum")
	if err == nil || fixture.ID == "" {
		return fixture, err
	}
	// Setup commits its base fixture before the nontransaction operation. Caller
	// cancellation or connection loss must not abandon that owned committed data.
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cleanupTarget, cleanupErr := pgx.ConnectConfig(cleanup, target.Config())
	if cleanupErr != nil {
		return fixture, errors.Join(err, cleanupErr)
	}
	defer closeVacuumConnection(cleanupTarget)
	cleanupState, cleanupErr := pgx.ConnectConfig(cleanup, state.Config())
	if cleanupErr != nil {
		return fixture, errors.Join(err, cleanupErr)
	}
	defer closeVacuumConnection(cleanupState)
	cleanupErr = RemoveConsumerFixture(cleanup, cleanupTarget, cleanupState, route, fixture.ID)
	return fixture, errors.Join(err, cleanupErr)
}

func createConsumerFixture(ctx context.Context, target, state *pgx.Conn, route, repair string) (report ConsumerFixture, err error) {
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
	if repair != "" {
		status, source = repair+"_ready", "ONLY "
		if err := writeTrialStatus(ctx, state, report.ID, repair+"_preparing", nil, nil); err != nil {
			return report, err
		}
		if repair == "vacuum" {
			err = seedVacuumFixture(ctx, target, report.Schema)
		} else if repair == "statistics" {
			report.OrdinaryAnalyzeMS, err = seedCorrelatedFixture(ctx, target, report.Schema)
		} else {
			err = seedStaleLookupStats(ctx, target, report.Schema)
		}
		if err != nil {
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
			var plan explain
			var err error
			if repair == "vacuum" && lookup.table == "candidate" {
				plan, err = readLookupPlan(ctx, tx, "EXPLAIN (FORMAT JSON, VERBOSE) "+vacuumQuery(report.Schema), int32(1), int32(50000))
			} else if repair == "statistics" && lookup.table == "candidate" {
				plan, err = readStatisticsPlan(ctx, tx, report.Schema, statisticsCases[0])
			} else {
				plan, err = readPlan(ctx, tx, "EXPLAIN (FORMAT JSON, VERBOSE) "+sql)
			}
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
		if repair == "vacuum" {
			var oid uint32
			err = target.QueryRow(ctx, "SELECT to_regclass($1)::oid", qualified(report.Schema, trialIndex)).Scan(&oid)
			if err == nil {
				err = writeVacuumStatus(ctx, state, status, &VacuumReport{Fixture: report.ID, IndexOID: oid})
			}
		} else {
			err = writeTrialStatus(ctx, state, report.ID, status, nil, nil)
		}
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
