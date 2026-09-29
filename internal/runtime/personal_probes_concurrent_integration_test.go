//go:build integration

package runtime

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPersonalProbeConcurrentCreateReplaysCommittedRequest(t *testing.T) {
	pool, h, _, session, csrf := childReviewFixture(t)
	ctx := context.Background()
	var targetID string
	if err := pool.QueryRow(ctx, `INSERT INTO tsw_target_accounts(identifier,identifier_hmac,identifier_key_version,display_label)
  VALUES('race@scope.test',decode(repeat('ab',32),'hex'),1,'race') RETURNING id`).Scan(&targetID); err != nil {
		t.Fatal(err)
	}
	selected := []uuid.UUID{uuid.MustParse(targetID)}
	preview := personalPreview(t, h, session, csrf, map[string]any{"targetAccountIds": selected})
	key := uuid.NewString()
	scope, ok := parsePersonalScope(&selected, nil)
	if !ok {
		t.Fatal("invalid fixture scope")
	}
	version, _ := h.keyRing.Current()
	fingerprint, err := personalScopeHash(scope, h.keyRing, version)
	if err != nil {
		t.Fatal(err)
	}
	competing, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer competing.Rollback(ctx)
	var existingID string
	if err = competing.QueryRow(ctx, `INSERT INTO tsw_personal_probe_batches(owner_id,request_key,scope_hash,scope_key_version,confirmed_scope_token,scope,scope_label,total)
 VALUES((SELECT id FROM tsw_owners LIMIT 1),$1,$2,$3,$4,$5,$6,1) RETURNING id`, key, fingerprint, version, preview.ScopeToken, scope.name, scope.label).Scan(&existingID); err != nil {
		t.Fatal(err)
	}
	if _, err = competing.Exec(ctx, `INSERT INTO tsw_personal_probe_items(batch_id,target_account_id,identifier) VALUES($1,$2,'race@scope.test')`, existingID, targetID); err != nil {
		t.Fatal(err)
	}
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response <- personalRequest(t, h, session, csrf, "POST", "/create", map[string]any{
			"targetAccountIds": selected, "expectedCount": 1, "confirmed": true, "requestKey": key, "scopeToken": preview.ScopeToken,
		})
	}()
	// The handler saw no committed key, then blocks on the competing unique insert.
	deadline := time.Now().Add(4 * time.Second)
	for {
		var blocked bool
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
   WHERE wait_event_type='Lock' AND query LIKE 'INSERT INTO tsw_personal_probe_batches%')`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("concurrent create did not reach unique-key conflict")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = competing.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-response:
		batch := decodePersonalBatch(t, got, 202)
		if batch.Id.String() != existingID || batch.Total != 1 || batch.Queued != 1 {
			t.Fatalf("concurrent replay mismatch: %+v", batch)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent create did not return after commit")
	}
	if _, err = pool.Exec(ctx, `UPDATE tsw_target_accounts SET version=version+1 WHERE id=$1`, targetID); err != nil {
		t.Fatal(err)
	}
	changed := personalPreview(t, h, session, csrf, map[string]any{"targetAccountIds": selected})
	if changed.ScopeToken == preview.ScopeToken {
		t.Fatal("version change retained preview token")
	}
	conflict := personalRequest(t, h, session, csrf, "POST", "/create", map[string]any{
		"targetAccountIds": selected, "expectedCount": 1, "confirmed": true, "requestKey": key, "scopeToken": changed.ScopeToken,
	})
	if conflict.Code != 409 {
		t.Fatalf("same request key accepted different confirmation: %d %s", conflict.Code, conflict.Body.String())
	}
}
