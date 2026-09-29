//go:build integration

package migrations

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// Rehearse the embedded 24 -> 25 -> 26 migrations with existing selection dependencies.
func TestOperationSelectionUpgradeFrom24Integration(t *testing.T) {
	dsn := os.Getenv("TSW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TSW_TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	files, err := fs.Sub(sqlFiles, "sql")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 24); err != nil {
		t.Fatal(err)
	}
	version, err := provider.GetDBVersion(ctx)
	if err != nil || version != 24 {
		t.Fatalf("before upgrade: version=%d err=%v", version, err)
	}
	owner, mother, workspace, batch, destination := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, fixture := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO tsw_owners(id,username,password_hash,totp_ciphertext,totp_nonce,totp_key_version) VALUES($1,'upgrade-owner','hash',decode(repeat('11',16),'hex'),decode(repeat('22',12),'hex'),1)`, []any{owner}},
		{`INSERT INTO tsw_mother_accounts(id,display_name) VALUES($1,'upgrade-mother')`, []any{mother}},
		{`INSERT INTO tsw_workspaces(id,platform_workspace_id,display_name) VALUES($1,$2,'upgrade-workspace')`, []any{workspace, workspace.String()}},
		{`INSERT INTO tsw_standby_child_batches(id,name) VALUES($1,'upgrade-batch')`, []any{batch}},
		{`INSERT INTO tsw_delivery_destinations(id,name,endpoint,target_group,secret_key_version,secret_nonce,secret_ciphertext) VALUES($1,'upgrade-destination','https://hub.fixture.test/api/v1','42',1,decode(repeat('33',12),'hex'),decode(repeat('44',32),'hex'))`, []any{destination}},
	} {
		if _, err := db.ExecContext(ctx, fixture.query, fixture.args...); err != nil {
			t.Fatal(err)
		}
	}
	run, generation := uuid.New(), uuid.New()
	var verification int64
	if err := db.QueryRowContext(ctx, `INSERT INTO tsw_workspace_verifications(workspace_id,mother_account_id,discovery_run_id,session_generation,secret_revision,token_attempt,token_exchange_id,source,outcome,permission,completeness,observed_at,expires_at) VALUES($1,$2,$3,$4,1,1,$5,'injected_platform_reader','verifying','unknown','unknown',now(),now()+interval '1 day') RETURNING id`, workspace, mother, run, generation, uuid.New()).Scan(&verification); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 25); err != nil {
		t.Fatal(err)
	}
	version, err = provider.GetDBVersion(ctx)
	if err != nil || version != 25 {
		t.Fatalf("before 25 -> 26 upgrade: version=%d err=%v", version, err)
	}
	var id uuid.UUID
	if err := db.QueryRowContext(ctx, `INSERT INTO tsw_operation_selection_drafts(owner_id,mother_account_id,mother_revision,workspace_id,visibility_run_id,session_generation,verification_id,batch_id,batch_version,destination_id,destination_revision) VALUES($1,$2,1,$3,$4,$5,$6,$7,1,$8,1) RETURNING id`, owner, mother, workspace, run, generation, verification, batch, destination).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	version, err = provider.GetDBVersion(ctx)
	if err != nil || version != RequiredVersion {
		t.Fatalf("after upgrade: version=%d err=%v", version, err)
	}
	if err := Apply(ctx, db); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='tsw_expiry_rotation_previews'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("migration 26 preview table missing: count=%d err=%v", count, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM tsw_operation_selection_drafts WHERE id=$1 AND owner_id=$2 AND mother_account_id=$3 AND workspace_id=$4 AND batch_id=$5 AND destination_id=$6`, id, owner, mother, workspace, batch, destination).Scan(&count); err != nil || count != 1 {
		t.Fatalf("idempotent upgrade lost selection dependencies: count=%d err=%v", count, err)
	}
}
