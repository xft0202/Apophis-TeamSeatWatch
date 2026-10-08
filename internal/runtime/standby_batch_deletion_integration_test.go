//go:build integration

package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

func standbyDeletionRequest(t *testing.T, h *OwnerAuthHandler, session, csrf, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set("Origin", "https://owner.test")
	req.Header.Set(auth.CSRFHeaderName, csrf)
	cookieToken := csrf
	if csrf == "wrong" {
		cookieToken = "fixture-csrf"
	}
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: cookieToken})
	if session != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
	}
	result := httptest.NewRecorder()
	ownerapi.Handler(h).ServeHTTP(result, req)
	return result
}

func TestStandbyBatchDeletionRetainsAccountsExecutionAndHistory(t *testing.T) {
	pool, h, owner, session, csrf := childReviewFixture(t)
	ctx := context.Background()
	var account, execution uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT target_account_id,batch_id FROM tsw_batch_memberships LIMIT 1`).Scan(&account, &execution); err != nil {
		t.Fatal(err)
	}
	accounts := []uuid.UUID{account}
	for _, identifier := range []string{"delete-two@example.test", "delete-three@example.test"} {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		item, err := h.insertTargetAccountTx(httptest.NewRequest("POST", "/", nil), tx, identifier, identifier, "password", "JBSWY3DPEHPK3PXP", "", "")
		if err != nil {
			tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		accounts = append(accounts, item.Id)
	}
	path := "/api/owner/v1/standby-child-batches"
	selection := decodeStandby[ownerapi.StandbyChildSelection](t, standbyRequest(t, h, session, csrf, "POST", path+"/selection", map[string]any{"scope": "selected", "accountIds": accounts}), 200)
	batch := decodeStandby[ownerapi.StandbyChildBatch](t, standbyRequest(t, h, session, csrf, "POST", path, map[string]any{"name": "保留历史批次", "selection": selection, "expectedCount": 3, "confirmed": true}), 201)
	if _, err := pool.Exec(ctx, `UPDATE tsw_batches SET source_standby_batch_id=$1,source_batch_name=$2 WHERE id=$3`, batch.Id, batch.Name, execution); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO tsw_rotation_global_protections(target_account_id,status,evidence_source,evidence_id,observed_at) VALUES($1,'delivered','fixture',repeat('d',64),now())`, account); err != nil {
		t.Fatal(err)
	}
	snapshots := map[string]string{}
	tables := []string{"tsw_target_accounts", "tsw_target_credentials", "tsw_batches", "tsw_batch_memberships", "tsw_operations", "tsw_operation_targets", "tsw_oauth_assets", "tsw_delivery_versions", "tsw_rotation_global_protections"}
	for _, table := range tables {
		var snapshot string
		if err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT coalesce(jsonb_agg(to_jsonb(record) ORDER BY to_jsonb(record)::text),'[]')::text FROM %s record`, table)).Scan(&snapshot); err != nil {
			t.Fatal(err)
		}
		snapshots[table] = snapshot
	}
	endpoint := path + "/" + batch.Id.String()
	body := map[string]any{"expectedVersion": batch.Version, "expectedCount": 3, "confirmed": true}
	for _, tc := range []struct {
		name          string
		session, csrf string
		body          any
		status        int
	}{
		{"unauthenticated", "", csrf, body, 401}, {"csrf", session, "wrong", body, 403},
		{"unconfirmed", session, csrf, map[string]any{"expectedVersion": batch.Version, "expectedCount": 3, "confirmed": false}, 422},
		{"missing count", session, csrf, map[string]any{"expectedVersion": batch.Version, "confirmed": true}, 422},
		{"negative count", session, csrf, map[string]any{"expectedVersion": batch.Version, "expectedCount": -1, "confirmed": true}, 422},
		{"stale version", session, csrf, map[string]any{"expectedVersion": batch.Version + 1, "expectedCount": 3, "confirmed": true}, 409},
		{"wrong count", session, csrf, map[string]any{"expectedVersion": batch.Version, "expectedCount": 0, "confirmed": true}, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := standbyDeletionRequest(t, h, tc.session, tc.csrf, "DELETE", endpoint, tc.body)
			if r.Code != tc.status {
				t.Fatalf("status=%d body=%s", r.Code, r.Body.String())
			}
		})
	}
	if r := standbyDeletionRequest(t, h, session, csrf, "DELETE", endpoint, body); r.Code != 204 {
		t.Fatalf("delete=%d %s", r.Code, r.Body.String())
	}
	var deleted bool
	var deletedBy string
	var version int64
	if err := pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL,deleted_by::text,version FROM tsw_standby_child_batches WHERE id=$1`, batch.Id).Scan(&deleted, &deletedBy, &version); err != nil || !deleted || deletedBy != owner || version != batch.Version+1 {
		t.Fatalf("deletion audit=%v %s %d %v", deleted, deletedBy, version, err)
	}
	var assigned *uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT batch_id,version FROM tsw_standby_child_memberships WHERE target_account_id=$1`, account).Scan(&assigned, &version); err != nil || assigned != nil || version != 2 {
		t.Fatalf("membership=%v v%d %v", assigned, version, err)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tsw_standby_child_memberships WHERE target_account_id=ANY($1) AND (batch_id IS NOT NULL OR version<>2)`, accounts).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("unreleased members=%d %v", remaining, err)
	}
	var history int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tsw_standby_child_history WHERE target_account_id=$1 AND previous_batch_id=$2 AND batch_id IS NULL AND owner_id=$3`, account, batch.Id, owner).Scan(&history); err != nil || history != 1 {
		t.Fatalf("history=%d %v", history, err)
	}
	for _, table := range tables {
		var after string
		if err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT coalesce(jsonb_agg(to_jsonb(record) ORDER BY to_jsonb(record)::text),'[]')::text FROM %s record`, table)).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if after != snapshots[table] {
			t.Errorf("deletion changed preserved records in %s", table)
		}
	}
	list := decodeStandby[ownerapi.StandbyChildBatchList](t, standbyDeletionRequest(t, h, session, csrf, "GET", path, nil), 200)
	if list.Total != 0 || len(list.Items) != 0 {
		t.Fatalf("deleted batch still listed: %+v", list)
	}
	for _, method := range []string{"GET", "DELETE"} {
		if r := standbyDeletionRequest(t, h, session, csrf, method, endpoint, body); r.Code != 404 {
			t.Errorf("%s deleted batch=%d %s", method, r.Code, r.Body.String())
		}
	}
	preview := standbyRequest(t, h, session, csrf, "POST", path+"/selection", map[string]any{"scope": "batch", "batchId": batch.Id})
	if preview.Code != 404 {
		t.Fatalf("deleted selection=%d %s", preview.Code, preview.Body.String())
	}
	rename := standbyRequest(t, h, session, csrf, "PATCH", endpoint+"/name", map[string]any{"name": "revived", "expectedVersion": batch.Version + 1})
	if rename.Code != 409 {
		t.Fatalf("deleted rename=%d %s", rename.Code, rename.Body.String())
	}
	released := decodeStandby[ownerapi.StandbyChildSelection](t, standbyRequest(t, h, session, csrf, "POST", path+"/selection", map[string]any{"scope": "selected", "accountIds": []uuid.UUID{account}}), 200)
	if released.Members[0].CurrentBatch != nil || released.Members[0].MembershipVersion != 2 {
		t.Fatalf("released account selection=%+v", released)
	}
	add := standbyRequest(t, h, session, csrf, "PATCH", endpoint, map[string]any{"name": batch.Name, "selection": released, "expectedCount": 1, "confirmed": true, "expectedVersion": batch.Version + 1, "action": "add"})
	if add.Code != 409 {
		t.Fatalf("deleted add=%d %s", add.Code, add.Body.String())
	}
	exported := standbyRequest(t, h, session, csrf, "POST", endpoint+"/export", map[string]any{"selection": map[string]any{"scope": "batch", "count": 0, "members": []any{}}, "expectedCount": 0, "expectedVersion": batch.Version + 1, "confirmed": true})
	if exported.Code != 409 {
		t.Fatalf("deleted export=%d %s", exported.Code, exported.Body.String())
	}
}

func TestStandbyBatchDeletionSupportsEmptyBatches(t *testing.T) {
	pool, h, _, session, csrf := childReviewFixture(t)
	ctx := context.Background()
	var batch uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO tsw_standby_child_batches(name) VALUES('空批次') RETURNING id`).Scan(&batch); err != nil {
		t.Fatal(err)
	}
	r := standbyDeletionRequest(t, h, session, csrf, "DELETE", "/api/owner/v1/standby-child-batches/"+batch.String(), map[string]any{"expectedVersion": 1, "expectedCount": 0, "confirmed": true})
	if r.Code != 204 {
		t.Fatalf("delete empty=%d %s", r.Code, r.Body.String())
	}
}

func TestStandbyBatchDeletionRacesTransferWithoutLosingAccount(t *testing.T) {
	pool, h, _, session, csrf := childReviewFixture(t)
	ctx := context.Background()
	var account uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM tsw_target_accounts LIMIT 1`).Scan(&account); err != nil {
		t.Fatal(err)
	}
	path := "/api/owner/v1/standby-child-batches"
	selected := decodeStandby[ownerapi.StandbyChildSelection](t, standbyRequest(t, h, session, csrf, "POST", path+"/selection", map[string]any{"scope": "selected", "accountIds": []uuid.UUID{account}}), 200)
	batch := decodeStandby[ownerapi.StandbyChildBatch](t, standbyRequest(t, h, session, csrf, "POST", path, map[string]any{"name": "race source", "selection": selected, "expectedCount": 1, "confirmed": true}), 201)
	selected = decodeStandby[ownerapi.StandbyChildSelection](t, standbyRequest(t, h, session, csrf, "POST", path+"/selection", map[string]any{"scope": "selected", "accountIds": []uuid.UUID{account}}), 200)
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err = blocker.Exec(ctx, `SELECT id FROM tsw_target_accounts WHERE id=$1 FOR UPDATE`, account); err != nil {
		t.Fatal(err)
	}
	var deleted, moved *httptest.ResponseRecorder
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		deleted = standbyDeletionRequest(t, h, session, csrf, "DELETE", path+"/"+batch.Id.String(), map[string]any{"expectedVersion": batch.Version, "expectedCount": 1, "confirmed": true})
	}()
	go func() {
		defer workers.Done()
		moved = standbyDeletionRequest(t, h, session, csrf, "POST", path, map[string]any{"name": "race destination", "selection": selected, "expectedCount": 1, "confirmed": true})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int
		if err = pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FROM tsw_target_accounts WHERE id=$1 FOR UPDATE%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("both requests did not reach their account lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	workers.Wait()
	if !((deleted.Code == 204 && moved.Code == 409) || (deleted.Code == 409 && moved.Code == 201)) {
		t.Fatalf("delete=%d %s; transfer=%d %s", deleted.Code, deleted.Body.String(), moved.Code, moved.Body.String())
	}
	var assigned *uuid.UUID
	if err = pool.QueryRow(ctx, `SELECT batch_id FROM tsw_standby_child_memberships WHERE target_account_id=$1`, account).Scan(&assigned); err != nil {
		t.Fatal(err)
	}
	if deleted.Code == 204 && assigned != nil {
		t.Fatalf("deleted batch retained assignment %v", assigned)
	}
	if moved.Code == 201 && (assigned == nil || *assigned == batch.Id) {
		t.Fatalf("successful transfer lost assignment %v", assigned)
	}
}
