//go:build integration

package task

import (
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/migrations"
	oauthdomain "github.com/xft0202/Apophis-TeamSeatWatch/internal/oauth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

func TestOAuthGenerationFailureSettlesAndAllowsExplicitRetry(t *testing.T) {
	dsn := os.Getenv("TSW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TSW_TEST_DATABASE_URL required")
	}
	ctx := t.Context()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.ExecContext(ctx, `DROP SCHEMA public CASCADE;CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err = migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ids := seedOAuthFenceGraph(t, ctx, pool)
	store := NewStore(pool, oauthIntegrationRing{})
	token := uuid.New()
	if _, err = pool.Exec(ctx, `UPDATE tsw_tasks SET lease_token=$2 WHERE id=$1`, ids.oldTask, token); err != nil {
		t.Fatal(err)
	}
	item := Task{ID: ids.oldTask, TaskType: "oauth_generate", OAuthAssetID: ids.asset, WorkspaceID: ids.workspace, MembershipID: ids.membership, LeaseToken: token, AttemptNo: 1, CorrelationID: "oauth-failure"}
	attempt, err := store.BeginDeliveryAttempt(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	failure := platform.DeliveryLiveness{Status: oauthdomain.ProbeUnknown, ErrorCode: "oauth_workspace_select_browser_challenge", Origin: "generation", ObservedAt: time.Now()}
	if err = store.FinishDeliveryAttempt(ctx, item, attempt, DeliveryTarget{MembershipID: ids.membership}, platform.DeliveryCredentialSet{}, failure); err != nil {
		t.Fatal(err)
	}
	var taskStatus, attemptStatus, reason string
	var terminal bool
	if err = pool.QueryRow(ctx, `SELECT t.status,t.finished_at IS NOT NULL,a.state,o.unavailable_reason FROM tsw_tasks t JOIN tsw_oauth_attempts a ON a.task_id=t.id JOIN tsw_oauth_assets o ON o.id=a.oauth_asset_id WHERE t.id=$1`, item.ID).Scan(&taskStatus, &terminal, &attemptStatus, &reason); err != nil {
		t.Fatal(err)
	}
	if taskStatus != "failed" || !terminal || attemptStatus != "failed" || reason != failure.ErrorCode {
		t.Fatalf("unsettled failure: %s %t %s %s", taskStatus, terminal, attemptStatus, reason)
	}
	item.ID = uuid.New().String()
	item.LeaseToken = uuid.New()
	if _, err = pool.Exec(ctx, `INSERT INTO tsw_tasks(id,membership_id,oauth_asset_id,workspace_id,task_type,dedupe_key,input_snapshot,correlation_id,status,lease_owner,lease_token,lease_expires_at,attempt_count,max_attempts) VALUES($1,$2,$3,$4,'oauth_generate',$6,'{}','explicit-retry','running','test',$5,now()+interval '10 minutes',1,3)`, item.ID, ids.membership, ids.asset, ids.workspace, item.LeaseToken, item.ID+":retry"); err != nil {
		t.Fatal(err)
	}
	retry, err := store.BeginDeliveryAttempt(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Generation != attempt.Generation+1 || retry.ID == attempt.ID {
		t.Fatal("explicit retry did not acquire a new fenced generation")
	}
	worker := Worker{Store: store, LeaseTime: 400 * time.Millisecond}
	authorization, stop, err := worker.keepOAuthLease(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	select {
	case <-authorization.Done():
		t.Fatal("authorization canceled before renewal check")
	case <-time.After(900 * time.Millisecond):
	}
	var valid bool
	if err = pool.QueryRow(ctx, `SELECT lease_expires_at>now() FROM tsw_tasks WHERE id=$1`, item.ID).Scan(&valid); err != nil || !valid {
		t.Fatalf("long OAuth grant lost its fence: %v", err)
	}
}
