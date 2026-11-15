package hostedquery

import (
	"context"
	"errors"
	"time"

	"pooltune/internal/supabaseproject"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type trafficPlanGrant struct {
	id        int64
	login     string
	tableOID  uint32
	column    int16
	grantedAt time.Time
}

func readProjectTrafficGrant(ctx context.Context, target *pgx.Conn, project supabaseproject.Project, claim investigationClaim, objective float64) (*trafficPlanGrant, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var grant trafficPlanGrant
	var database uint32
	var generation [3]time.Time
	var currentObjective float64
	err := target.QueryRow(ctx, "SELECT * FROM "+qualified(project.Role(), "read_traffic_plan_grant")+"($1::oid,$2::bigint)", claim.Role, claim.Query).Scan(
		&grant.id, &grant.login, &grant.tableOID, &grant.column, &database, &currentObjective, &generation[0], &generation[1], &generation[2], &grant.grantedAt)
	var pgErr *pgconn.PgError
	if errors.Is(err, pgx.ErrNoRows) || (errors.As(err, &pgErr) && (pgErr.Code == "42501" || pgErr.Code == "42883")) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if grant.id <= 0 || grant.tableOID == 0 || grant.column <= 0 || grant.grantedAt.IsZero() || !directLoginName.MatchString(grant.login) ||
		database != claim.Database || currentObjective != objective || len(generation) != len(claim.Generation) {
		return nil, nil
	}
	for i, at := range generation {
		if at.IsZero() || !at.Equal(claim.Generation[i]) {
			return nil, nil
		}
	}
	return &grant, nil
}
