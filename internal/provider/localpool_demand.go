package provider

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// Waiting clients belong to the one allowed application scope. Even an empty
// foreign pool invalidates this deliberately single-scope management profile.
func (p *LocalPool) waiting(ctx context.Context, c *pgx.Conn) (int, error) {
	rows, err := poolRows(ctx, c, "SHOW POOLS")
	if err != nil {
		return 0, err
	}
	waiting := 0
	for _, r := range rows {
		if r["database"] == "pgbouncer" {
			continue
		}
		if r["database"] != p.cfg.Application.Database || r["user"] != p.cfg.Application.User || r["pool_mode"] != "transaction" {
			return 0, ErrUnsupported
		}
		n, e := poolInt(r["cl_waiting"])
		if e != nil {
			return 0, e
		}
		waiting += n
		if waiting > 1000000 {
			return 0, ErrUnsupported
		}
	}
	return waiting, nil
}
