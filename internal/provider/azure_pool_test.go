package provider

import (
	"context"
	"github.com/jackc/pgx/v5"
	"testing"
	"time"
)

func TestManagedPoolCountsExcludeOtherRolesAndRejectUnknownPools(t *testing.T) {
	rows := []map[string]string{{"database": "postgres", "user": "app", "pool_mode": "transaction", "sv_active": "4", "sv_idle": "1", "cl_waiting": "3", "sv_used": "0", "sv_tested": "0", "sv_login": "0"}, {"database": "postgres", "user": "azuresu", "pool_mode": "transaction", "sv_active": "0", "sv_idle": "1", "cl_waiting": "0"}}
	w, i, q, e := azurePoolCounts(rows, "postgres", "app")
	if e != nil || w != 5 || i != 1 || q != 3 {
		t.Fatalf("wrong inventory: %d %d %d %v", w, i, q, e)
	}
	rows = append(rows, map[string]string{"database": "postgres", "user": "foreign", "pool_mode": "transaction"})
	if _, _, _, e = azurePoolCounts(rows, "postgres", "app"); e == nil {
		t.Fatal("foreign application pool accepted")
	}
}
func TestManagedReconciliationDoesNotDemandFilledCeilingAfterQuietShrink(t *testing.T) {
	// A ceiling is not a reservation: 3 actual connections safely fit a new ceiling 4.
	op := Operation{Value: 4, StartedAt: time.Now()}
	if !azureCapacityConfirmed(op, 5, 4, 3, 0) {
		t.Fatal("safe drained shrink wedged")
	}
	if azureCapacityConfirmed(op, 5, 4, 5, 0) {
		t.Fatal("old physical capacity marked drained")
	}
	if azureCapacityConfirmed(op, 3, 4, 3, 0) {
		t.Fatal("growth accepted without usable capacity")
	}
	if !azureCapacityConfirmed(op, 3, 4, 4, 0) {
		t.Fatal("actual grown capacity rejected")
	}
}

func TestAzurePoolRequiresExclusiveManagementAndResourceBoundTLS(t *testing.T) {
	c := azureProfile(t)
	c.ExclusiveManagement = false
	if _, e := NewAzurePool(c); e == nil {
		t.Fatal("writer without exclusive management approval")
	}
	c.ExclusiveManagement = true
	c.Monitor.TLSConfig.InsecureSkipVerify = true
	if _, e := NewAzurePool(c); e == nil {
		t.Fatal("unverified managed host accepted")
	}
}

func azureProfile(t *testing.T) AzurePoolConfig {
	t.Helper()
	a, e := pgx.ParseConfig("host=instance.postgres.database.azure.com port=6432 user=app dbname=pgbouncer sslmode=verify-full")
	if e != nil {
		t.Fatal(e)
	}
	m := a.Copy()
	m.Port = 5432
	m.Database = "postgres"
	return AzurePoolConfig{API: Config{Kind: Azure, Target: Target{ID: "db"}, Subscription: "sub", ResourceGroup: "group", Instance: "instance", Timeout: time.Second, Freshness: time.Second, Token: func(context.Context) (string, error) { return "token", nil }}, Admin: a, Monitor: m, Database: "postgres", Role: "app", Maximum: 8, OtherReserve: 20, Journal: &memoryJournal{}, ExclusiveManagement: true}
}
