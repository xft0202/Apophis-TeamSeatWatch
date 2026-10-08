//go:build integration

package writerfence

import (
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The same connection must keep both shadows alive while trigger DML and the
// confirmation seam run; separate pooled connections would miss the attack.
func TestEpochTempShadowCannotSpoofDurableKeysIntegration(t *testing.T) {
	_, ctx := epochFixture(t)
	config, err := pgxpool.ParseConfig(os.Getenv("TSW_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	account, space, otherSpace, mother := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	run := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	run(`INSERT INTO public.tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,'shadow@fixture.test',decode(repeat('88',32),'hex'),1,'shadow')`, account)
	run(`INSERT INTO public.tsw_mother_accounts(id,display_name) VALUES($1,'mother')`, mother)
	run(`INSERT INTO public.tsw_workspaces(id,platform_workspace_id,display_name) VALUES($1,$2,'real'),($3,$4,'fake')`, space, space.String(), otherSpace, otherSpace.String())
	// No implicit schema selection: the durable table must really exist in public.
	var namespace string
	if err := pool.QueryRow(ctx, `SELECT n.nspname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.oid='public.tsw_rotation_epochs'::regclass`).Scan(&namespace); err != nil || namespace != "public" {
		t.Fatalf("durable epoch schema=%s: %v", namespace, err)
	}
	run(`CREATE TEMP TABLE tsw_rotation_epochs(kind text,id uuid,version bigint) ON COMMIT PRESERVE ROWS`)
	run(`INSERT INTO pg_temp.tsw_rotation_epochs(kind,id,version) VALUES('target_account',$1,777),('workspace',$2,777)`, account, space)
	run(`CREATE TEMP TABLE tsw_workspace_verifications(id bigint PRIMARY KEY,workspace_id uuid) ON COMMIT PRESERVE ROWS`)
	run(`SET search_path=pg_temp,public`)
	run(`UPDATE public.tsw_target_accounts SET status='disabled',version=version+1 WHERE id=$1`, account)
	// A caller must not directly advance a real epoch through the public helper.
	if _, err := pool.Exec(ctx, `SELECT public.tsw_rotation_epoch_advance('target_account',$1)`, account); err == nil {
		t.Fatal("direct epoch helper invocation succeeded")
	}
	tx, versions, err := BeginLocked(ctx, pool, []Key{{TargetAccount, account}})
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions[0].Version != 2 {
		tx.Rollback(ctx)
		t.Fatalf("BeginLocked read shadow version: %+v", versions)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var verification int64
	err = pool.QueryRow(ctx, `INSERT INTO public.tsw_workspace_verifications(workspace_id,mother_account_id,discovery_run_id,session_generation,secret_revision,token_attempt,token_exchange_id,source,outcome,permission,completeness,observed_at,expires_at) VALUES($1,$2,$3,$4,1,1,$5,'injected_platform_reader','verifying','unknown','unknown',now(),now()+interval '1 day') RETURNING id`, space, mother, uuid.New(), uuid.New(), uuid.New()).Scan(&verification)
	if err != nil {
		t.Fatal(err)
	}
	run(`INSERT INTO pg_temp.tsw_workspace_verifications(id,workspace_id) VALUES($1,$2)`, verification, otherSpace)
	run(`INSERT INTO public.tsw_workspace_verification_entries(verification_id,kind,identifier,identifier_hmac,identifier_key_version,status,platform_member_id) VALUES($1,'member','member@fixture.test',decode(repeat('89',32),'hex'),1,'active','member-shadow')`, verification)
	for _, item := range []struct {
		key  Key
		want int64
	}{{Key{TargetAccount, account}, 2}, {Key{Workspace, space}, 3}, {Key{Workspace, otherSpace}, 1}} {
		var version int64
		if err := pool.QueryRow(ctx, `SELECT version FROM public.tsw_rotation_epochs WHERE kind=$1 AND id=$2`, item.key.Kind, item.key.ID).Scan(&version); err != nil || version != item.want {
			t.Fatalf("durable epoch %v=%d want %d: %v", item.key, version, item.want, err)
		}
	}
	var shadowAccount, shadowSpace int64
	if err := pool.QueryRow(ctx, `SELECT a.version,w.version FROM pg_temp.tsw_rotation_epochs a,pg_temp.tsw_rotation_epochs w WHERE a.kind='target_account' AND a.id=$1 AND w.kind='workspace' AND w.id=$2`, account, space).Scan(&shadowAccount, &shadowSpace); err != nil || shadowAccount != 777 || shadowSpace != 777 {
		t.Fatalf("temp shadow changed or unreadable: %d %d %v", shadowAccount, shadowSpace, err)
	}
}
