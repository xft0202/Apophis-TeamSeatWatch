//go:build integration

package runtime

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/migrations"
)

func TestRotationJoinDispatchProtectedRollbackPreservesOriginalSources(t *testing.T) {
	f, _, _, _ := dispatchFixture(t)
	ctx := context.Background()
	before := executionMigrationSources(t, f)
	db, err := sql.Open("pgx", os.Getenv("TSW_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, os.DirFS("../migrations/sql"))
	if err != nil {
		t.Fatal(err)
	}
	assertProtectedMigrationRollbacks(t, 31)
	if before != executionMigrationSources(t, f) {
		t.Fatal("rejected rollback changed original intent or source payload")
	}
	for i := 0; i < 2; i++ {
		if err = migrations.Apply(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	version, err := provider.GetDBVersion(ctx)
	if err != nil || version != migrations.RequiredVersion {
		t.Fatalf("up=%d required=%d err=%v", version, migrations.RequiredVersion, err)
	}
	var evidencePresent bool
	if err = f.pool.QueryRow(ctx, `SELECT to_regclass('public.tsw_rotation_join_membership_evidence') IS NOT NULL`).Scan(&evidencePresent); err != nil || !evidencePresent {
		t.Fatalf("membership evidence up cleanup present=%v err=%v", evidencePresent, err)
	}
	var triggers int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger WHERE tgname='tsw_rotation_candidate_personal_gate' AND tgrelid IN ('public.tsw_target_personal_access'::regclass,'public.tsw_target_personal_sessions'::regclass) AND tgenabled='O'`).Scan(&triggers); err != nil || triggers != 2 {
		t.Fatalf("triggers=%d err=%v", triggers, err)
	}
	if before != executionMigrationSources(t, f) {
		t.Fatal("upgrade changed source payload or epoch")
	}
}
