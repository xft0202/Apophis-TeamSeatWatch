//go:build integration

package runtime

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

func TestBusinessTimelinesNewestFirstBeforePagination(t *testing.T) {
	f := newCardRevocationFixture(t)
	ctx := t.Context()
	newerMember := seedCardListMember(t, f, 1, true)
	if response := publicRedeemRequest(t, f.public, http.MethodPost, "/api/public/v1/redeem/confirm", map[string]any{"cardSecret": f.secret}, nil); response.Code != 200 {
		t.Fatalf("mock redemption failed: %d %s", response.Code, response.Body.String())
	}
	var card, order, mother, batch, target uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT c.id,o.id,b.mother_account_id,m.batch_id,m.join_operation_target_id FROM tsw_cards c JOIN tsw_orders o ON o.card_id=c.id JOIN tsw_batch_memberships m ON m.id=c.membership_id JOIN tsw_batches x ON x.id=m.batch_id JOIN tsw_mother_workspace_bindings b ON b.id=x.binding_id WHERE m.id=$1`, f.ids.membership).Scan(&card, &order, &mother, &batch, &target); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2030, 1, 2, 3, 0, 0, 0, time.UTC)
	if _, err := f.pool.Exec(ctx, `INSERT INTO tsw_audit_events(event_key,retention_scope_type,retention_scope_id,actor_type,event_type,entity_type,entity_id,outcome,correlation_id,details,occurred_at,expires_at)
 SELECT digest('time-order-'||n,'sha256'),'card',$1::uuid,'system','public.reclaim_updated','card',$1::uuid,'succeeded','time-order',jsonb_build_object('action','reclaim_status','status','checking'),$2::timestamptz+n*interval '1 second',$2::timestamptz+n*interval '1 second'+interval '7 days' FROM generate_series(1,120) n`, card, base); err != nil {
		t.Fatal(err)
	}
	handler := ownerapi.Handler(f.owner)
	get := func(path string, result any) {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, cardOwnerRequest(f, http.MethodGet, path, nil, false))
		if w.Code != 200 {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), result); err != nil {
			t.Fatal(err)
		}
	}
	var detail ownerapi.DeliveryRecord
	get("/api/owner/v1/deliveries/"+f.ids.membership, &detail)
	if detail.Timeline == nil || len(*detail.Timeline) != 100 || !(*detail.Timeline)[0].OccurredAt.Equal(base.Add(120*time.Second)) || !(*detail.Timeline)[99].OccurredAt.Equal(base.Add(21*time.Second)) {
		t.Fatal("card detail must return the latest 100 events, newest first")
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	first, err := redemptionTimeline(ctx, tx, order, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	second, err := redemptionTimeline(ctx, tx, order, 2, 20)
	if err != nil {
		t.Fatal(err)
	}
	if first.Total < 120 || len(first.Items) != 20 || len(second.Items) != 20 || !first.Items[0].OccurredAt.Equal(base.Add(120*time.Second)) || !second.Items[0].OccurredAt.Equal(base.Add(100*time.Second)) {
		t.Fatal("redemption timeline must sort the whole history before pagination")
	}
	if !first.Items[19].OccurredAt.After(second.Items[0].OccurredAt) {
		t.Fatal("redemption history pages overlap or run oldest first")
	}
	public, err := loadTimelineTx(ctx, tx, card.String())
	if err != nil || len(public) != 100 || !public[0].OccurredAt.Equal(base.Add(120*time.Second)) || !public[99].OccurredAt.Equal(base.Add(21*time.Second)) {
		t.Fatalf("customer history must return the newest events: count=%d error=%v", len(public), err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var cards ownerapi.DeliveryRecordList
	get(fmt.Sprintf("/api/owner/v1/deliveries?cards_only=true&mother_account_id=%s&workspace_id=%s", mother, f.ids.workspace), &cards)
	if len(cards.Items) != 2 || cards.Items[0].MembershipId.String() != newerMember {
		t.Fatal("a recently redeemed old card must not move ahead of a newly generated card")
	}
	var newestAccount uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT target_account_id FROM tsw_batch_memberships WHERE id=$1`, newerMember).Scan(&newestAccount); err != nil {
		t.Fatal(err)
	}
	var accounts ownerapi.TargetAccountList
	get("/api/owner/v1/target-accounts?page=1&page_size=20&sort=created_desc", &accounts)
	if len(accounts.Items) != 2 || accounts.Items[0].Id != newestAccount {
		t.Fatal("the most recently imported account must appear first")
	}
	var deliveries ownerapi.DeliveryList
	get("/api/owner/v1/batches/"+batch.String()+"/deliveries", &deliveries)
	if len(deliveries.Items) != 2 || deliveries.Items[0].MembershipId.String() != newerMember {
		t.Fatal("account delivery records must show the newest records first")
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO tsw_audit_events(event_key,retention_scope_type,retention_scope_id,actor_type,event_type,entity_type,entity_id,outcome,correlation_id,details,occurred_at,expires_at)
 SELECT digest('time-receipt-'||n,'sha256'),'workspace',$1::uuid,'system','join.stage_finished','operation_target',$2::uuid,'succeeded','time-receipt',jsonb_build_object('stage','send_invitation','http_status',200+n,'success',true,'request_sent',true,'request_may_have_effect',false,'diagnostic_code',''),$3::timestamptz+n*interval '1 second',$3::timestamptz+n*interval '1 second'+interval '7 days' FROM generate_series(1,2) n`, f.ids.workspace, target, base); err != nil {
		t.Fatal(err)
	}
	var operation ownerapi.JoinOperation
	get("/api/owner/v1/batches/"+batch.String()+"/join-operation", &operation)
	if len(operation.Targets) != 2 || len(operation.Targets[0].StageResults) != 2 || operation.Targets[0].StageResults[0].HttpStatus != 202 {
		t.Fatal("the invitation result must use the latest receipt while retaining frozen account order")
	}
}
