//go:build integration

package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/audit"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

func TestRedemptionRecordsIntegration(t *testing.T) {
	if !strings.Contains(os.Getenv("TSW_TEST_DATABASE_URL"), "/tsw_redemptions_test?") {
		t.Fatal("requires disposable tsw_redemptions_test database")
	}
	f := newCardRevocationFixture(t)
	ctx := context.Background()
	var mother, batch, card uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT binding.mother_account_id,batch.id,card.id FROM tsw_batch_memberships m JOIN tsw_batches batch ON batch.id=m.batch_id JOIN tsw_mother_workspace_bindings binding ON binding.id=batch.binding_id JOIN tsw_cards card ON card.membership_id=m.id WHERE m.id=$1`, f.ids.membership).Scan(&mother, &batch, &card); err != nil {
		t.Fatal(err)
	}
	source := uuid.New()
	if _, err := f.pool.Exec(ctx, `INSERT INTO tsw_standby_child_batches(id,name) VALUES ($1,'十月批次')`, source); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE tsw_batches SET source_standby_batch_id=$2,source_batch_name='十月批次' WHERE id=$1`, batch, source); err != nil {
		t.Fatal(err)
	}
	claim := func(secret string) {
		t.Helper()
		r := publicRedeemRequest(t, f.public, "POST", "/api/public/v1/redeem/confirm", map[string]any{"cardSecret": secret}, nil)
		if r.Code != 200 {
			t.Fatalf("claim %d %s", r.Code, r.Body.String())
		}
	}
	claim(f.secret)
	members := []string{f.ids.membership}
	for n := 1; n < 25; n++ {
		members = append(members, seedCardListMember(t, f, n, true))
		claim("TSW1-" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{byte(n)}, 20)))
	}
	seedCardListMember(t, f, 25, false)
	other, _ := seedOtherWorkspaceDelivery(t, f)
	handler := ownerapi.Handler(f.owner)
	serve := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, cardOwnerRequest(f, "GET", path, nil, false))
		return w
	}
	scope := fmt.Sprintf("mother_account_id=%s&workspace_id=%s", mother, f.ids.workspace)
	list := func(extra string) ownerapi.RedemptionRecordList {
		t.Helper()
		w := serve("/api/owner/v1/redemptions?" + scope + extra)
		if w.Code != 200 {
			t.Fatalf("list %d %s", w.Code, w.Body.String())
		}
		var v ownerapi.RedemptionRecordList
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	detail := func(extra string) ownerapi.RedemptionRecordDetail {
		t.Helper()
		w := serve("/api/owner/v1/redemptions/" + f.ids.membership + "?" + scope + extra)
		if w.Code != 200 {
			t.Fatalf("detail %d %s", w.Code, w.Body.String())
		}
		for _, secret := range []string{f.secret, "access_token", "refresh_token", "password_secret", "payload"} {
			if bytes.Contains(w.Body.Bytes(), []byte(secret)) {
				t.Fatalf("detail exposes %s", secret)
			}
		}
		var v ownerapi.RedemptionRecordDetail
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	first := list("")
	if first.Total != 25 || len(first.Items) != 20 {
		t.Fatalf("first page %d/%d", first.Total, len(first.Items))
	}
	if v := list("&page=2"); v.Total != 25 || len(v.Items) != 5 {
		t.Fatal("incorrect second page")
	}
	for _, size := range []int{50, 100} {
		if v := list(fmt.Sprintf("&page_size=%d", size)); v.Total != 25 || len(v.Items) != 25 {
			t.Fatal("incorrect page size")
		}
	}
	if v := list("&search=account007"); v.Total != 1 || v.Items[0].MembershipId.String() != members[7] {
		t.Fatal("search mixed objects")
	}
	if v := list("&source_batch_id=" + source.String()); v.Total != 25 {
		t.Fatal("source batch mismatch")
	}
	if v := list("&source_batch_id=" + uuid.NewString()); v.Total != 0 {
		t.Fatal("source escaped scope")
	}
	for _, path := range []string{"/api/owner/v1/redemptions", "/api/owner/v1/redemptions?" + scope + "&credential_state=wrong", "/api/owner/v1/redemptions?" + scope + "&redeemed_from=2026-10-02T00:00:00Z&redeemed_before=2026-10-01T00:00:00Z"} {
		if w := serve(path); w.Code != 400 {
			t.Fatalf("invalid filter %d", w.Code)
		}
	}
	if w := serve("/api/owner/v1/redemptions/" + other.membership + "?" + scope); w.Code != 404 {
		t.Fatal("detail escaped workspace")
	}
	r := cardOwnerRequest(f, "GET", "/api/owner/v1/redemptions?"+scope, nil, false)
	r.Header.Del("Cookie")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("unauthenticated list accepted")
	}
	sources := serve("/api/owner/v1/redemptions/batches?" + scope)
	if sources.Code != 200 || !bytes.Contains(sources.Body.Bytes(), []byte("十月批次")) {
		t.Fatalf("sources %d %s", sources.Code, sources.Body.String())
	}
	original := detail("")
	if original.OriginalDelivery == nil || original.OriginalDelivery.Generation != 1 || original.CurrentDelivery.Generation != 1 {
		t.Fatal("original not pinned")
	}
	var order uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT id FROM tsw_orders WHERE membership_id=$1`, f.ids.membership).Scan(&order); err != nil {
		t.Fatal(err)
	}
	if v := list("&redeemed_from=" + original.Record.RedeemedAt.Add(time.Second).UTC().Format(time.RFC3339Nano)); v.Total >= 25 {
		t.Fatal("date filter not applied")
	}
	// Terminal failures retain uncertainty; only an explicit unrecoverable result permits reauthorization.
	taskID := uuid.New()
	if _, err := f.pool.Exec(ctx, `INSERT INTO tsw_tasks(id,workspace_id,membership_id,oauth_asset_id,task_type,dedupe_key,input_snapshot,status,correlation_id,finished_at,reclaim_result,reclaim_stage)
 VALUES ($1,$2,$3,$4,'oauth_reclaim','redemption-matrix',jsonb_build_object('origin','customer','order_id',$5::text,'delivery_version_id',$6::text),'failed','redemption-test',now(),'timeout','publish')`, taskID, f.ids.workspace, f.ids.membership, f.ids.asset, order, f.ids.deliveryVersion); err != nil {
		t.Fatal(err)
	}
	matrix := []struct {
		status, result, asset, liveness string
		state                           ownerapi.RedemptionReclaimState
		authorize                       bool
	}{
		{"failed", "timeout", "unavailable", "unknown", ownerapi.RedemptionReclaimStatePendingCheck, false},
		{"interrupted", "unknown", "unavailable", "unknown", ownerapi.RedemptionReclaimStatePendingCheck, false},
		{"queued", "", "reclaiming", "auth_error", ownerapi.RedemptionReclaimStateQueued, false},
		{"retry_wait", "unknown", "reclaiming", "auth_error", ownerapi.RedemptionReclaimStateRunning, false},
		{"succeeded", "probe_ok", "ready", "ok", ownerapi.RedemptionReclaimStateHealthy, false},
		{"succeeded", "token_refresh", "ready", "ok", ownerapi.RedemptionReclaimStateRestored, false},
		{"failed", "unrecoverable", "unavailable", "auth_error", ownerapi.RedemptionReclaimStateUnrecoverable, true},
	}
	for _, c := range matrix {
		if _, err := f.pool.Exec(ctx, `UPDATE tsw_tasks SET status=$2,reclaim_result=NULLIF($3,''),finished_at=CASE WHEN $2 IN ('succeeded','failed','interrupted') THEN now() ELSE NULL END WHERE id=$1`, taskID, c.status, c.result); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(ctx, `UPDATE tsw_oauth_assets SET status=$2,liveness_status=$3 WHERE id=$1`, f.ids.asset, c.asset, c.liveness); err != nil {
			t.Fatal(err)
		}
		v := detail("")
		if v.Record.ReclaimState != c.state || v.Record.CanAuthorizeReclaim != c.authorize {
			t.Fatalf("state %s %+v", c.result, v.Record)
		}
		filtered := list("&membership_id=" + f.ids.membership + "&reclaim_state=" + string(c.state) + "&credential_state=" + string(v.Record.CredentialState))
		if filtered.Total != 1 {
			t.Fatal("filter and row facts disagree")
		}
	}
	accepted := authorizeDeliveryReclaimIntegrationRequest(t, f.owner, f.ids.membership, f.sessionToken, f.csrfToken, "redemption-retry-1", true)
	if accepted.Code != 202 {
		t.Fatal(accepted.Body.String())
	}
	retry := authorizeDeliveryReclaimIntegrationRequest(t, f.owner, f.ids.membership, f.sessionToken, f.csrfToken, "redemption-retry-1", true)
	if retry.Code != 202 {
		t.Fatal("lost response duplicated")
	}
	v := detail("")
	if v.Record.ReclaimState != ownerapi.RedemptionReclaimStateQueued || v.Record.CanAuthorizeReclaim || v.Reclaims.Items[0].Origin != "owner" || v.Reclaims.Items[0].PreviousGeneration == nil {
		t.Fatal("queued history incorrect")
	}
	// A new published delivery replaces current credentials while retaining the original order and version.
	replacement := uuid.New()
	hash := sha256.Sum256([]byte("{}"))
	if _, err := f.pool.Exec(ctx, `INSERT INTO tsw_delivery_versions(id,oauth_asset_id,generation,payload,payload_sha256,validated_platform_subject_id,validated_workspace_id) VALUES ($1,$2,2,'{}',$3,'subject',$4)`, replacement, f.ids.asset, hash[:], f.ids.workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE tsw_oauth_assets SET status='ready',current_generation=2,current_delivery_version_id=$2,liveness_status='ok' WHERE id=$1`, f.ids.asset, replacement); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE tsw_orders SET current_delivery_version_id=$2 WHERE id=$1`, order, replacement); err != nil {
		t.Fatal(err)
	}
	v = detail("")
	if v.OriginalDelivery.Generation != 1 || v.CurrentDelivery.Generation != 2 || v.Record.OrderId != order {
		t.Fatal("original redemption overwritten")
	}
	if _, err := f.pool.Exec(ctx, `UPDATE tsw_orders SET original_delivery_version_id=$2 WHERE id=$1`, order, replacement); err == nil {
		t.Fatal("original version is mutable")
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for n := 0; n < 25; n++ {
		if _, err = audit.Write(ctx, tx, audit.Event{Type: audit.PublicReclaimUpdated, Actor: audit.ActorSystem, RetentionScopeID: card.String(), EntityType: "card", EntityID: card.String(), Outcome: audit.OutcomeSucceeded, CorrelationID: "redemption-history", Details: audit.PublicAccessDetails{Action: "reclaim_status", Result: "checking", Status: "probe"}, IdempotencyKey: fmt.Sprintf("redemption-history-%d", n)}); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if v = detail("&timeline_page=2"); v.Timeline.Total < 25 || len(v.Timeline.Items) == 0 {
		t.Fatal("timeline was silently truncated")
	}
	if r := revokeCardIntegrationRequest(t, f.owner, f.ids.membership, f.sessionToken, f.csrfToken, true, true); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	v = detail("")
	if !v.Record.CardRevoked || v.Record.AccessAvailable || v.Record.CanAuthorizeReclaim || v.Record.CredentialState != ownerapi.RedemptionCredentialStateRevoked || v.Record.RedeemedAt != original.Record.RedeemedAt {
		t.Fatal("revocation rewrote historical redemption")
	}
	t.Log("verified scoped 20/50/100 pagination, filters, original/current history, reclaim authorization, timeline pagination, and secret-free detail")
}
