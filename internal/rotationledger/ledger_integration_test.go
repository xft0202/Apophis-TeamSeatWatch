//go:build integration

package rotationledger

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/migrations"
)

func TestUsageAndGlobalProtectionAreMonotonicAndAdvanceTargetEpoch(t *testing.T) {
	dsn := os.Getenv("TSW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TSW_TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err = migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	db.Close()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	account, workspace := uuid.New(), uuid.New()
	if _, err = pool.Exec(ctx, `INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,'ledger@example.test',decode(repeat('11',32),'hex'),1,'Ledger')`, account); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO tsw_workspaces(id,platform_workspace_id,display_name) VALUES($1,$2,'Ledger workspace')`, workspace, workspace.String()); err != nil {
		t.Fatal(err)
	}
	store := Store{Pool: pool}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err = store.RecordUsage(ctx, UsageEvidence{TargetAccountID: account, WorkspaceID: workspace, State: "never_used", Source: "workspace_usage_probe", EvidenceID: hashLiteral('a'), ObservedAt: now, ExpiresAt: now.Add(5 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.RecordUsage(ctx, UsageEvidence{TargetAccountID: account, WorkspaceID: workspace, State: "unknown", Source: "workspace_usage_probe", EvidenceID: hashLiteral('b'), ObservedAt: now.Add(time.Second), ExpiresAt: now.Add(5 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	usage, err := store.GetUsage(ctx, account, workspace)
	if err != nil || usage == nil || usage.State != "never_used" || usage.EverUsed {
		t.Fatalf("unknown downgraded evidence: usage=%+v err=%v", usage, err)
	}
	if _, err = store.RecordUsage(ctx, UsageEvidence{TargetAccountID: account, WorkspaceID: workspace, State: "used", Source: "workspace_usage_probe", EvidenceID: hashLiteral('c'), ObservedAt: now.Add(2 * time.Second), ExpiresAt: now.Add(5 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	usage, err = store.GetUsage(ctx, account, workspace)
	if err != nil || usage == nil || usage.State != "used" || !usage.EverUsed {
		t.Fatalf("used evidence not sticky: usage=%+v err=%v", usage, err)
	}
	if _, err = store.SetProtection(ctx, ProtectionEvidence{TargetAccountID: account, Status: "suspected_sold", Source: "manual_review", EvidenceID: hashLiteral('d'), ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SetProtection(ctx, ProtectionEvidence{TargetAccountID: account, Status: "none", Source: "manual_review", EvidenceID: hashLiteral('e'), ObservedAt: now.Add(time.Second)}); err != nil {
		t.Fatal("reviewed transient protection could not be cleared:", err)
	}
	if _, err = store.SetProtection(ctx, ProtectionEvidence{TargetAccountID: account, Status: "delivered", Source: "customer_delivery", EvidenceID: hashLiteral('f'), ObservedAt: now.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SetProtection(ctx, ProtectionEvidence{TargetAccountID: account, Status: "sale_reserved", Source: "customer_delivery", EvidenceID: hashLiteral('7'), ObservedAt: now.Add(3 * time.Second)}); err == nil {
		t.Fatal("permanent delivered protection was changed")
	}
	protection, err := store.GetProtection(ctx, account)
	if err != nil || protection == nil || protection.Status != "delivered" {
		t.Fatalf("protection=%+v err=%v", protection, err)
	}
	var epoch int64
	if err = pool.QueryRow(ctx, `SELECT version FROM tsw_rotation_epochs WHERE kind='target_account' AND id=$1`, account).Scan(&epoch); err != nil || epoch < 7 {
		t.Fatalf("target epoch=%d err=%v", epoch, err)
	}
}

func hashLiteral(value byte) string {
	out := make([]byte, 64)
	const hex = "0123456789abcdef"
	for i := range out {
		out[i] = hex[int(value)%len(hex)]
	}
	return string(out)
}
