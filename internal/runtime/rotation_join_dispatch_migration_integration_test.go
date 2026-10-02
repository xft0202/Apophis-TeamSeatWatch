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

func TestRotationJoinDispatchMigrationDownUp(t *testing.T) {
	f, _, _, _ := dispatchFixture(t)
	ctx := context.Background()
	before := executionSources(t, f)
	db, err := sql.Open("pgx", os.Getenv("TSW_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, os.DirFS("../migrations/sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.Down(ctx); err != nil {
		t.Fatal(err)
	}
	version, err := provider.GetDBVersion(ctx)
	if err != nil || version != 31 {
		t.Fatalf("down=%d err=%v", version, err)
	}
	var absent bool
	if err = f.pool.QueryRow(ctx, `SELECT to_regprocedure('public.tsw_rotation_candidate_personal_gate()') IS NULL AND NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgname='tsw_rotation_candidate_personal_gate')`).Scan(&absent); err != nil || !absent {
		t.Fatalf("down cleanup absent=%v err=%v", absent, err)
	}
	if before != executionSources(t, f) {
		t.Fatal("downgrade changed source payload or original intent")
	}
	for i := 0; i < 2; i++ {
		if err = migrations.Apply(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	version, err = provider.GetDBVersion(ctx)
	if err != nil || version != 32 || migrations.RequiredVersion != 32 {
		t.Fatalf("up=%d required=%d err=%v", version, migrations.RequiredVersion, err)
	}
	var triggers int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger WHERE tgname='tsw_rotation_candidate_personal_gate' AND tgrelid IN ('public.tsw_target_personal_access'::regclass,'public.tsw_target_personal_sessions'::regclass) AND tgenabled='O'`).Scan(&triggers); err != nil || triggers != 2 {
		t.Fatalf("triggers=%d err=%v", triggers, err)
	}
	if before != executionSources(t, f) {
		t.Fatal("upgrade changed source payload or epoch")
	}
}
