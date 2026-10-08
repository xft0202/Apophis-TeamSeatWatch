//go:build integration

package task

import (
	"context"
	"database/sql"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/migrations"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTaskConcurrencyIntegration(t *testing.T) {
	dsn := os.Getenv("TSW_TEST_DATABASE_URL")
	u, err := url.Parse(dsn)
	if dsn == "" {
		t.Skip("isolated database required")
	}
	if err != nil || u.Hostname() != "127.0.0.1" || !strings.HasPrefix(u.Path, "/tsw_proxy_verify_") {
		t.Fatal("requires isolated proxy verification database")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE;CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ids := seedOAuthFenceGraph(t, ctx, pool)
	if _, err := pool.Exec(ctx, `DELETE FROM tsw_tasks`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tsw_batches SET login_task_concurrency=2 WHERE id=$1`, ids.batch); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := pool.Exec(ctx, `INSERT INTO tsw_tasks(membership_id,oauth_asset_id,workspace_id,task_type,dedupe_key,input_snapshot,correlation_id) VALUES($1,$2,$3,'oauth_generate',$4,'{}','concurrency-fixture')`, ids.membership, ids.asset, ids.workspace, uuid.New().String()); err != nil {
			t.Fatal(err)
		}
	}
	var inherited int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tsw_tasks WHERE concurrency_key=$1 AND concurrency_limit=2`, "login:"+ids.batch).Scan(&inherited); err != nil || inherited != 8 {
		t.Fatal("login task scope not inherited")
	}
	store := NewStore(pool, oauthIntegrationRing{})
	var claimed atomic.Int32
	var wait sync.WaitGroup
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := store.ClaimDelivery(ctx, uuid.NewString(), time.Minute, "oauth_generate", AttemptRoute{Mode: "direct"})
			if err == nil {
				claimed.Add(1)
			} else if !errors.Is(err, pgx.ErrNoRows) {
				t.Errorf("claim failed: %v", err)
			}
		}()
	}
	wait.Wait()
	if claimed.Load() != 2 {
		t.Fatalf("racing claims=%d expected2", claimed.Load())
	}
	if _, err := store.NextNetworkTaskType(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("saturated group remained schedulable")
	}
	if _, err := pool.Exec(ctx, `UPDATE tsw_tasks SET lease_expires_at=now()-interval '1 second' WHERE id=(SELECT id FROM tsw_tasks WHERE status='running' LIMIT 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimDelivery(ctx, "recovery", time.Minute, "oauth_generate", AttemptRoute{Mode: "direct"}); err != nil {
		t.Fatal("expired lease did not restore capacity", err)
	}
	var running int
	pool.QueryRow(ctx, `SELECT count(*) FROM tsw_tasks WHERE status='running' AND lease_expires_at>now()`).Scan(&running)
	if running != 2 {
		t.Fatalf("recovery exceeded concurrency:%d", running)
	}
}
