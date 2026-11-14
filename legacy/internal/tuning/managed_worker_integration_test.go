//go:build managedlive && integration

package tuning

import (
	"context"
	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
	"testing"
)

func TestLiveWorkerReplacesClosedConnectionForNextRequest(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t)
	c, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	c.Close(context.Background())
	if e = executeLiveRead(ctx, &c, cfg); e != nil {
		t.Fatalf("new request inherited a dead connection: %v", e)
	}
	defer c.Close(context.Background())
}
