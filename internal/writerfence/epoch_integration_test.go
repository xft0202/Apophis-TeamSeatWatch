//go:build integration

package writerfence

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/migrations"
)

func epochFixture(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	dsn := os.Getenv("TSW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("disposable TSW_TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err = migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, ctx
}
func execEpoch(t *testing.T, ctx context.Context, db *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}
func epochVersion(t *testing.T, ctx context.Context, db *pgxpool.Pool, key Key) int64 {
	t.Helper()
	var v int64
	if err := db.QueryRow(ctx, `SELECT version FROM tsw_rotation_epochs WHERE kind=$1 AND id=$2`, key.Kind, key.ID).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}
func TestEpochFactTransitionsIntegration(t *testing.T) {
	db, ctx := epochFixture(t)
	mother, space, account, batch, otherBatch, destination := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	execEpoch(t, ctx, db, `INSERT INTO tsw_mother_accounts(id,display_name) VALUES($1,'mother')`, mother)
	execEpoch(t, ctx, db, `INSERT INTO tsw_workspaces(id,platform_workspace_id,display_name) VALUES($1,$2,'space')`, space, space.String())
	execEpoch(t, ctx, db, `INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,'target@fixture.test',decode(repeat('11',32),'hex'),1,'target')`, account)
	execEpoch(t, ctx, db, `INSERT INTO tsw_standby_child_batches(id,name) VALUES($1,'first'),($2,'second')`, batch, otherBatch)
	execEpoch(t, ctx, db, `INSERT INTO tsw_delivery_destinations(id,name,endpoint,target_group,secret_key_version,secret_nonce,secret_ciphertext) VALUES($1,'dest','https://fixture.test','42',1,decode(repeat('22',12),'hex'),decode(repeat('33',32),'hex'))`, destination)
	w := Key{Workspace, space}
	a := Key{TargetAccount, account}
	b := Key{StandbyBatch, batch}
	b2 := Key{StandbyBatch, otherBatch}
	d := Key{Destination, destination}
	run, gen, exchange := uuid.New(), uuid.New(), uuid.New()
	var verification int64
	if err := db.QueryRow(ctx, `INSERT INTO tsw_workspace_verifications(workspace_id,mother_account_id,discovery_run_id,session_generation,secret_revision,token_attempt,token_exchange_id,source,outcome,permission,completeness,observed_at,expires_at) VALUES($1,$2,$3,$4,1,1,$5,'injected_platform_reader','verifying','unknown','unknown',now(),now()+interval '1 day') RETURNING id`, space, mother, run, gen, exchange).Scan(&verification); err != nil {
		t.Fatal(err)
	}
	if epochVersion(t, ctx, db, w) != 2 {
		t.Fatal("new verification was not fenced")
	}
	execEpoch(t, ctx, db, `UPDATE tsw_workspace_verifications SET outcome='failed' WHERE id=$1`, verification)
	if epochVersion(t, ctx, db, w) != 3 {
		t.Fatal("verification completion was not fenced")
	}
	// A later attempt supersedes the selected id without updating its row.
	execEpoch(t, ctx, db, `INSERT INTO tsw_workspace_verifications(workspace_id,mother_account_id,discovery_run_id,session_generation,secret_revision,token_attempt,token_exchange_id,source,outcome,permission,completeness,observed_at,expires_at) VALUES($1,$2,$3,$4,1,1,$5,'injected_platform_reader','verifying','unknown','unknown',now(),now()+interval '1 day')`, space, mother, run, gen, exchange)
	if epochVersion(t, ctx, db, w) != 4 {
		t.Fatal("newer verification phantom was not fenced")
	}
	otherSpace := uuid.New()
	execEpoch(t, ctx, db, `INSERT INTO tsw_workspaces(id,platform_workspace_id,display_name) VALUES($1,$2,'other')`, otherSpace, otherSpace.String())
	execEpoch(t, ctx, db, `INSERT INTO tsw_selected_workspace_tokens(mother_account_id,workspace_id,discovery_run_id,session_generation,secret_revision,exchange_id,status) VALUES($1,$2,$3,$4,1,$5,'exchanging')`, mother, space, run, gen, exchange)
	execEpoch(t, ctx, db, `UPDATE tsw_selected_workspace_tokens SET workspace_id=$2 WHERE mother_account_id=$1 AND workspace_id=$3`, mother, otherSpace, space)
	if epochVersion(t, ctx, db, w) != 6 || epochVersion(t, ctx, db, Key{Workspace, otherSpace}) != 2 {
		t.Fatal("old/new workspace keys on token reassignment were not fenced")
	}
	execEpoch(t, ctx, db, `UPDATE tsw_delivery_destinations SET revision=revision+1,test_connection=NULL,test_target=NULL,test_revision=NULL,tested_at=NULL WHERE id=$1`, destination)
	execEpoch(t, ctx, db, `UPDATE tsw_delivery_destinations SET test_attempt=test_attempt+1 WHERE id=$1`, destination)
	execEpoch(t, ctx, db, `UPDATE tsw_delivery_destinations SET test_connection='connected',test_target='connected',test_revision=revision,tested_at=now() WHERE id=$1`, destination)
	if epochVersion(t, ctx, db, d) != 4 {
		t.Fatal("destination update and both test phases were not fenced")
	}
	execEpoch(t, ctx, db, `INSERT INTO tsw_target_credentials(target_account_id,password_secret,material_status) VALUES($1,'password','needs_totp')`, account)
	execEpoch(t, ctx, db, `UPDATE tsw_target_credentials SET password_secret='new password',secret_revision=2,version=2 WHERE target_account_id=$1`, account)
	if epochVersion(t, ctx, db, a) != 3 {
		t.Fatal("credential insertion/rotation was not fenced")
	}
	execEpoch(t, ctx, db, `INSERT INTO tsw_standby_child_memberships(target_account_id,batch_id) VALUES($1,$2)`, account, batch)
	// The standby editor advances both batch versions in the same transfer transaction.
	transfer, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE tsw_standby_child_memberships SET batch_id=$2,version=version+1 WHERE target_account_id=$1`, []any{account, otherBatch}},
		{`UPDATE tsw_standby_child_batches SET version=version+1 WHERE id=$1`, []any{batch}},
		{`UPDATE tsw_standby_child_batches SET version=version+1 WHERE id=$1`, []any{otherBatch}},
	} {
		if _, err = transfer.Exec(ctx, step.sql, step.args...); err != nil {
			_ = transfer.Rollback(ctx)
			t.Fatal(err)
		}
	}
	if err = transfer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if epochVersion(t, ctx, db, a) != 3 || epochVersion(t, ctx, db, b) != 2 || epochVersion(t, ctx, db, b2) != 2 {
		t.Fatal("editor's old/new batch version mutations did not advance batch epochs")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE tsw_delivery_destinations SET test_attempt=test_attempt+1 WHERE id=$1`, destination); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if epochVersion(t, ctx, db, d) != 4 {
		t.Fatal("rollback advanced epoch")
	}
}
func TestEpochLockSerializationIntegration(t *testing.T) {
	db, ctx := epochFixture(t)
	id := uuid.New()
	execEpoch(t, ctx, db, `INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,'lock@fixture.test',decode(repeat('55',32),'hex'),1,'lock')`, id)
	first, _, err := BeginLocked(ctx, db, []Key{{TargetAccount, id}})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Rollback(ctx)
	writer, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := writer.Exec(ctx, `UPDATE tsw_target_accounts SET status='disabled',version=version+1 WHERE id=$1`, id)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("writer passed held epoch: %v", err)
	case <-time.After(120 * time.Millisecond):
	}
	if err = first.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("epoch writer blocked")
	}
	if err = writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if epochVersion(t, ctx, db, Key{TargetAccount, id}) != 2 {
		t.Fatal("writer did not advance version")
	}
}

// The public lock interface refuses absent keys, rather than creating an unfenced phantom.
func TestEpochMissingKeyIntegration(t *testing.T) {
	db, ctx := epochFixture(t)
	tx, _, err := BeginLocked(ctx, db, []Key{{Workspace, uuid.New()}})
	if err == nil {
		tx.Rollback(ctx)
		t.Fatal("missing epoch accepted")
	}
	if err == pgx.ErrNoRows {
		t.Fatal("missing key should identify its scope")
	}
}
