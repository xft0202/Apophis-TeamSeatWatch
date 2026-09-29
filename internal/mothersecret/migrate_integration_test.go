//go:build integration

package mothersecret

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/migrations"
)

func setupMaterialPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	dsn := os.Getenv("TSW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TSW_TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
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

func seedLegacyMother(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, hash byte) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO tsw_mother_accounts(id,display_name) VALUES ($1,'legacy')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO tsw_mother_account_credentials(mother_account_id,login_identifier,identifier_hmac,identifier_key_version,password_secret,totp_secret) VALUES ($1,$2,$3,1,'legacy-password','JBSWY3DPEHPK3PXP')`, id, id.String()+"@example.test", bytes.Repeat([]byte{hash}, 32)); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateLegacyMotherCredentialsExclusiveIdempotentAndFailClosed(t *testing.T) {
	pool, ctx := setupMaterialPool(t)
	id := uuid.New()
	seedLegacyMother(t, ctx, pool, id, 1)
	generation := uuid.New()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO tsw_mother_personal_access(mother_account_id,secret_revision,status) VALUES ($1,1,'ready')`, []any{id}},
		{`INSERT INTO tsw_mother_personal_sessions(mother_account_id,secret_revision,generation,key_version,nonce,sealed_session,expires_at) VALUES ($1,1,$2,3,$3,$4,now()+interval '1 hour')`, []any{id, generation, bytes.Repeat([]byte{1}, 12), bytes.Repeat([]byte{2}, 32)}},
		{`INSERT INTO tsw_mother_discoveries(mother_account_id,run_id,secret_revision,session_generation,status) VALUES ($1,$2,1,$3,'discovered')`, []any{id, uuid.New(), generation}},
	} {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- Migrate(ctx, pool, testKeyRing{key: [32]byte{3}}) }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var password, totp []byte
	var revision, version int64
	if err := pool.QueryRow(ctx, `SELECT password_secret,totp_secret,secret_revision,version FROM tsw_mother_account_credentials WHERE mother_account_id=$1`, id).Scan(&password, &totp, &revision, &version); err != nil {
		t.Fatal(err)
	}
	if !IsSealed(password) || !IsSealed(totp) || bytes.Contains(password, []byte("legacy-password")) || revision != 2 || version != 2 {
		t.Fatalf("migration not once: revision=%d version=%d", revision, version)
	}
	var stale int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM tsw_mother_personal_access WHERE mother_account_id=$1)+(SELECT count(*) FROM tsw_mother_personal_sessions WHERE mother_account_id=$1)+(SELECT count(*) FROM tsw_mother_discoveries WHERE mother_account_id=$1)`, id).Scan(&stale); err != nil || stale != 0 {
		t.Fatalf("migration retained old Personal generation: count=%d err=%v", stale, err)
	}
	if p, err := Open(testKeyRing{key: [32]byte{3}}, id, revision, Password, password); err != nil || string(p) != "legacy-password" {
		t.Fatalf("password roundtrip failed: %v", err)
	}
	if p, err := Open(testKeyRing{key: [32]byte{3}}, id, revision, TOTP, totp); err != nil || string(p) != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("totp roundtrip failed: %v", err)
	}
	if err := Migrate(ctx, pool, testKeyRing{key: [32]byte{8}}); err == nil {
		t.Fatal("missing/dead key accepted after migration")
	}
	bad := append([]byte(nil), password...)
	bad[len(bad)-1] ^= 1
	if _, err := pool.Exec(ctx, `UPDATE tsw_mother_account_credentials SET password_secret=$2,secret_revision=secret_revision+1,version=version+1 WHERE mother_account_id=$1`, id, bad); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool, testKeyRing{key: [32]byte{3}}); err == nil {
		t.Fatal("tampered envelope accepted at startup")
	}
	if _, err := pool.Exec(ctx, `UPDATE tsw_mother_account_credentials SET password_secret='plaintext',secret_revision=secret_revision+1,version=version+1 WHERE mother_account_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool, testKeyRing{key: [32]byte{3}}); err == nil {
		t.Fatal("plaintext fallback after marker")
	}
}

func TestMigrateRollbackOnTamperedPreexistingEnvelope(t *testing.T) {
	pool, ctx := setupMaterialPool(t)
	first, second := uuid.New(), uuid.New()
	seedLegacyMother(t, ctx, pool, first, 2)
	seedLegacyMother(t, ctx, pool, second, 3)
	if _, err := pool.Exec(ctx, `UPDATE tsw_mother_account_credentials SET password_secret=$2,secret_revision=secret_revision+1,version=version+1 WHERE mother_account_id=$1`, second, []byte("TSWM1\x00broken-ciphertext")); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool, testKeyRing{key: [32]byte{3}}); err == nil {
		t.Fatal("tampered preexisting envelope accepted")
	}
	var raw []byte
	var marker bool
	if err := pool.QueryRow(ctx, `SELECT password_secret FROM tsw_mother_account_credentials WHERE mother_account_id=$1`, first).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tsw_mother_material_crypto_state WHERE id=true)`).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if string(raw) != "legacy-password" || marker {
		t.Fatalf("partial migration committed: marker=%v", marker)
	}
}
