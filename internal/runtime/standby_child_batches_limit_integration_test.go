//go:build integration

package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	targetdomain "github.com/xft0202/Apophis-TeamSeatWatch/internal/target"
)

// The pool has more than the hard batch limit; two separate valid changes
// must not produce an unpreviewable or unexportable 10001-member batch.
func TestStandbyChildHardLimitIntegration(t *testing.T) {
	pool, h, _, session, csrf := childReviewFixture(t)
	ctx := context.Background()
	password, err := targetdomain.SealMaterial("batch-pass", h.keyRing)
	if err != nil {
		t.Fatal(err)
	}
	totp, err := targetdomain.SealMaterial("JBSWY3DPEHPK3PXP", h.keyRing)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO tsw_target_accounts(identifier,identifier_hmac,identifier_key_version,display_label)
 SELECT 'bulk05-'||lpad(n::text,5,'0')||'@example.test',decode(lpad(to_hex(n),64,'0'),'hex'),1,'bulk'
 FROM generate_series(1,10003) n`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO tsw_target_credentials(target_account_id,password_secret,totp_secret,material_status,materials_sealed)
 SELECT id,$1,$2,'complete',true FROM tsw_target_accounts WHERE identifier LIKE 'bulk05-%'`, password, totp)
	if err != nil {
		t.Fatal(err)
	}
	ids := []uuid.UUID{}
	rows, err := pool.Query(ctx, `SELECT id FROM tsw_target_accounts WHERE identifier LIKE 'bulk05-%' ORDER BY identifier`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(ids) != 10003 {
		t.Fatalf("fixture size=%d", len(ids))
	}
	path := "/api/owner/v1/standby-child-batches"
	filtered := standbyRequest(t, h, session, csrf, "POST", path+"/selection", map[string]any{"scope": "filtered", "search": "bulk05-"})
	var limit struct {
		Code        string `json:"code"`
		ActualCount int64  `json:"actualCount"`
	}
	if err = json.Unmarshal(filtered.Body.Bytes(), &limit); err != nil {
		t.Fatal(err)
	}
	if filtered.Code != 409 || limit.Code != "range_limit_exceeded" || limit.ActualCount != 10003 {
		t.Fatalf("filter scope must disclose actual count: %d %s", filtered.Code, filtered.Body.String())
	}
	selected := func(accounts []uuid.UUID) ownerapi.StandbyChildSelection {
		return decodeStandby[ownerapi.StandbyChildSelection](t, standbyRequest(t, h, session, csrf, "POST", path+"/selection", map[string]any{"scope": "selected", "accountIds": accounts}), 200)
	}
	save := func(batch *ownerapi.StandbyChildBatch, selection ownerapi.StandbyChildSelection) *httptest.ResponseRecorder {
		body := map[string]any{"name": "Bounded", "selection": selection, "expectedCount": selection.Count, "confirmed": true}
		if batch == nil {
			return standbyRequest(t, h, session, csrf, "POST", path, body)
		}
		body["expectedVersion"] = batch.Version
		body["action"] = "add"
		return standbyRequest(t, h, session, csrf, "PATCH", path+"/"+batch.Id.String(), body)
	}
	batch := decodeStandby[ownerapi.StandbyChildBatch](t, save(nil, selected(ids[:5000])), 201)
	batch = decodeStandby[ownerapi.StandbyChildBatch](t, save(&batch, selected(ids[5000:9999])), 200)
	if batch.MemberCount != 9999 {
		t.Fatalf("two additions should have 9999 members: %+v", batch)
	}
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, id := range ids[9999:10001] {
		frozen := selected([]uuid.UUID{id})
		wg.Add(1)
		go func() { defer wg.Done(); codes <- save(&batch, frozen).Code }()
	}
	wg.Wait()
	close(codes)
	success, conflict := 0, 0
	for code := range codes {
		if code == 200 {
			success++
		}
		if code == 409 {
			conflict++
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("race success=%d conflict=%d", success, conflict)
	}
	listing := decodeStandby[[]ownerapi.StandbyChildBatch](t, standbyRequest(t, h, session, csrf, "GET", path, nil), 200)
	if len(listing) != 1 || listing[0].MemberCount != 10000 {
		t.Fatalf("hard limit after race: %+v", listing)
	}
	batch = listing[0]
	for _, id := range ids[9999:10001] {
		frozen := selected([]uuid.UUID{id})
		if frozen.Members[0].MembershipVersion != 0 {
			continue
		}
		over := save(&batch, frozen)
		if over.Code != 409 || !strings.Contains(over.Body.String(), `"code":"range_limit_exceeded"`) || !strings.Contains(over.Body.String(), `"actualCount":10001`) {
			t.Fatalf("overflow add=%d %s", over.Code, over.Body.String())
		}
	}
	// A transfer from another batch must also fail without deleting its assignment.
	source := decodeStandby[ownerapi.StandbyChildBatch](t, save(nil, selected(ids[10001:10002])), 201)
	transfer := save(&batch, selected(ids[10001:10002]))
	if transfer.Code != 409 || !strings.Contains(transfer.Body.String(), `"actualCount":10001`) {
		t.Fatalf("overflow transfer=%d %s", transfer.Code, transfer.Body.String())
	}
	var held uuid.UUID
	if err = pool.QueryRow(ctx, `SELECT batch_id FROM tsw_standby_child_memberships WHERE target_account_id=$1`, ids[10001]).Scan(&held); err != nil || held != source.Id {
		t.Fatalf("transfer changed source membership: %s %v", held, err)
	}
	frozenBatch := decodeStandby[ownerapi.StandbyChildSelection](t, standbyRequest(t, h, session, csrf, "POST", path+"/selection", map[string]any{"scope": "batch", "batchId": batch.Id}), 200)
	if frozenBatch.Count != 10000 {
		t.Fatalf("full batch cannot preview: %d", frozenBatch.Count)
	}
	exported := standbyRequest(t, h, session, csrf, "POST", fmt.Sprintf("%s/%s/export", path, batch.Id), map[string]any{"selection": frozenBatch, "expectedCount": 10000, "expectedVersion": batch.Version, "confirmed": true})
	if exported.Code != 200 || strings.Count(exported.Body.String(), "\n") != 10000 {
		t.Fatalf("full batch cannot export: %d lines=%d", exported.Code, strings.Count(exported.Body.String(), "\n"))
	}
	// A repeated addition of a present member remains idempotent at capacity.
	batch = decodeStandby[ownerapi.StandbyChildBatch](t, save(&batch, selected(ids[:1])), 200)
	if batch.MemberCount != 10000 {
		t.Fatalf("same-batch addition changed count: %+v", batch)
	}
	// Rejected transfer did not append history; a later valid transfer remains possible.
	var sourceHistory int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM tsw_standby_child_history WHERE target_account_id=$1`, ids[10001]).Scan(&sourceHistory); err != nil || sourceHistory != 1 {
		t.Fatalf("rejected transfer changed history: %d %v", sourceHistory, err)
	}
	removed := selected(ids[:1])
	batch = decodeStandby[ownerapi.StandbyChildBatch](t, standbyRequest(t, h, session, csrf, "PATCH", path+"/"+batch.Id.String(), map[string]any{"name": batch.Name, "selection": removed, "expectedCount": 1, "confirmed": true, "expectedVersion": batch.Version, "action": "remove"}), 200)
	if batch.MemberCount != 9999 {
		t.Fatalf("removal failed: %+v", batch)
	}
	batch = decodeStandby[ownerapi.StandbyChildBatch](t, save(&batch, selected(ids[10001:10002])), 200)
	if batch.MemberCount != 10000 {
		t.Fatalf("valid transfer after removal failed: %+v", batch)
	}
}

func TestStandbyChildBatchIgnoresEmptyDomainIntegration(t *testing.T) {
	pool, h, _, session, csrf := childReviewFixture(t)
	password, err := targetdomain.SealMaterial("pass", h.keyRing)
	if err != nil {
		t.Fatal(err)
	}
	var id uuid.UUID
	err = pool.QueryRow(context.Background(), `INSERT INTO tsw_target_accounts(identifier,identifier_hmac,identifier_key_version,display_label)
 VALUES ('username',decode(repeat('fd',32),'hex'),1,'username') RETURNING id`).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(context.Background(), `INSERT INTO tsw_target_credentials(target_account_id,password_secret,material_status,materials_sealed) VALUES ($1,$2,'needs_totp',true)`, id, password)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/owner/v1/standby-child-batches"
	selection := decodeStandby[ownerapi.StandbyChildSelection](t, standbyRequest(t, h, session, csrf, "POST", path+"/selection", map[string]any{"scope": "selected", "accountIds": []uuid.UUID{id}}), 200)
	batch := decodeStandby[ownerapi.StandbyChildBatch](t, standbyRequest(t, h, session, csrf, "POST", path, map[string]any{"name": "Username", "selection": selection, "expectedCount": 1, "confirmed": true}), 201)
	if batch.MemberCount != 1 || batch.DomainCount != 0 || len(batch.Domains) != 0 {
		t.Fatalf("empty domain counted: %+v", batch)
	}
	listed := decodeStandby[[]ownerapi.StandbyChildBatch](t, standbyRequest(t, h, session, csrf, "GET", path, nil), 200)
	if len(listed) != 1 || listed[0].DomainCount != 0 || len(listed[0].Domains) != 0 {
		t.Fatalf("empty domain listed: %+v", listed)
	}
}
