package hostedquery

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

// The protocol backend key can name a session-pooler proxy instead of a server
// backend. Guard admission must obtain the database PID through a SQL reader.
func TestVacuumGuardPIDReadsPostgresBackend(t *testing.T) {
	db := &guardPIDReader{}
	pid, err := vacuumGuardPID(context.Background(), db)
	if err != nil || pid != 168326 || db.calls != 1 {
		t.Fatalf("database guard PID=%d calls=%d error=%v", pid, db.calls, err)
	}
	db.fail = true
	if _, err := vacuumGuardPID(context.Background(), db); err == nil {
		t.Fatal("missing backend identity accepted")
	}
}

type guardPIDReader struct {
	calls int
	fail  bool
}

func (r *guardPIDReader) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	r.calls++
	return guardPIDRow{valid: sql == "SELECT pg_backend_pid()" && !r.fail}
}

type guardPIDRow struct{ valid bool }

func (r guardPIDRow) Scan(dest ...any) error {
	if !r.valid {
		return errors.New("server PID unavailable")
	}
	*dest[0].(*uint32) = 168326
	return nil
}
