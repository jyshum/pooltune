package runtime

import (
	"context"
	"github.com/jackc/pgx/v5"
	"pooltune/internal/durable"
)

// AcquireTuningOwner shares the existing physical-cluster lease with both
// legacy traffic profiles. A background tuner cannot race a local proxy owner.
func AcquireTuningOwner(ctx context.Context, monitor *pgx.ConnConfig) (*durable.Files, error) {
	return acquireLocalDatabaseOwner(ctx, monitor)
}
