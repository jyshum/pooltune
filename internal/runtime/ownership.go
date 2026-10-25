package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/jackc/pgx/v5"
	"os"
	"path/filepath"
	"pooltune/internal/control"
	"pooltune/internal/durable"
	"time"
)

// acquireLocalDatabaseOwner fences same-host, same-OS-owner local profiles by
// verified PostgreSQL cluster identity, independently of configured target names, endpoint
// aliases, ingress paths and state directories. This is not a distributed lease
// or a restart/backup epoch anchor. Never unlink a lock while a process may own it.
func acquireLocalDatabaseOwner(ctx context.Context, monitor *pgx.ConnConfig) (*durable.Files, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, e := pgx.ConnectConfig(ctx, monitor)
	if e != nil {
		return nil, control.ErrRecovery
	}
	var cluster, database string
	e = conn.QueryRow(ctx, "SELECT system_identifier::text, (SELECT oid::text FROM pg_database WHERE datname=current_database()) FROM pg_control_system()").Scan(&cluster, &database)
	conn.Close(ctx)
	if e != nil || cluster == "" || database == "" {
		return nil, control.ErrRecovery
	}
	// max_connections is cluster-wide even when database OIDs differ. The
	// isolated local profile cannot coordinate separate budgets within a cluster.
	digest := sha256.Sum256([]byte(cluster))
	// Fixed host location, intentionally independent of TMPDIR and runtime JSON.
	// OS accounts are separate deployment authorities; do not share local profiles
	// across accounts or hosts, or delete these lock files as a recovery shortcut.
	root := fmt.Sprintf("/tmp/pooltune-local-owners-%d", os.Getuid())
	if e = os.Mkdir(root, 0700); e != nil && !os.IsExist(e) {
		return nil, control.ErrRecovery
	}
	info, e := os.Lstat(root)
	if e != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, control.ErrRecovery
	}
	directory := filepath.Join(root, hex.EncodeToString(digest[:]))
	if e = os.Mkdir(directory, 0700); e != nil && !os.IsExist(e) {
		return nil, control.ErrRecovery
	}
	owner, e := durable.Open(directory)
	if e != nil {
		return nil, control.ErrRecovery
	}
	return owner, nil
}
