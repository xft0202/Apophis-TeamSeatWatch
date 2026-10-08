//go:build integration

package writerfence

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/migrations"
)

func TestIdentityAndStandaloneMembershipPhantomsAdvanceStableEpochs(t *testing.T) {
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
	account, batch := uuid.New(), uuid.New()
	if _, err = pool.Exec(ctx, `INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,'same@example.test',decode(repeat('11',32),'hex'),1,'first')`, account); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO tsw_standby_child_batches(id,name) VALUES($1,'standby')`, batch); err != nil {
		t.Fatal(err)
	}
	before := readEpochs(t, pool, []Key{{Kind: TargetIdentity, ID: TargetIdentityID}, {Kind: StandbyMembership, ID: StandbyMembershipID}, {Kind: TargetAccount, ID: account}, {Kind: StandbyBatch, ID: batch}})
	if _, err = pool.Exec(ctx, `INSERT INTO tsw_target_accounts(identifier,identifier_hmac,identifier_key_version,display_label) VALUES('same@example.test',decode(repeat('22',32),'hex'),2,'duplicate key generation')`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO tsw_standby_child_memberships(target_account_id,batch_id) VALUES($1,$2)`, account, batch); err != nil {
		t.Fatal(err)
	}
	after := readEpochs(t, pool, []Key{{Kind: TargetIdentity, ID: TargetIdentityID}, {Kind: StandbyMembership, ID: StandbyMembershipID}, {Kind: TargetAccount, ID: account}, {Kind: StandbyBatch, ID: batch}})
	for key, version := range before {
		if after[key] <= version {
			t.Fatalf("epoch did not advance for %s/%s: before=%d after=%d", key.Kind, key.ID, version, after[key])
		}
	}
}

func readEpochs(t *testing.T, pool *pgxpool.Pool, keys []Key) map[Key]int64 {
	t.Helper()
	out := make(map[Key]int64, len(keys))
	for _, key := range keys {
		var version int64
		if err := pool.QueryRow(context.Background(), `SELECT version FROM public.tsw_rotation_epochs WHERE kind=$1 AND id=$2`, key.Kind, key.ID).Scan(&version); err != nil {
			t.Fatal(err)
		}
		out[key] = version
	}
	return out
}
