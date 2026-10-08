//go:build integration

package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
	targetdomain "github.com/xft0202/Apophis-TeamSeatWatch/internal/target"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/task"
)

func TestOperationPreparationAndExplicitLoginIntegration(t *testing.T) {
	pool, h, ownerID, session, csrf := childReviewFixture(t)
	ctx := context.Background()
	mother, space, batch, child, other := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	run, generation, exchange := uuid.New(), uuid.New(), uuid.New()
	access := fixtureWorkspaceAccess(space.String())
	key, nonce, sealed, err := sealWorkspaceAccess(h.keyRing, workspaceAccessBinding{motherID: mother, workspaceID: space, run: run, generation: generation, revision: 1, exchangeID: exchange, attempt: 1}, access)
	if err != nil {
		t.Fatal(err)
	}
	h.selectedWorkspaceReader = &selectedFixture{}
	sealedPassword, err := targetdomain.SealMaterial("password", h.keyRing)
	if err != nil {
		t.Fatal(err)
	}
	sealedTotp, err := targetdomain.SealMaterial("JBSWY3DPEHPK3PXP", h.keyRing)
	if err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tsw_mother_accounts(id,display_name) VALUES($1,'mother')`, []any{mother}},
		{`INSERT INTO tsw_mother_account_credentials(mother_account_id,login_identifier,identifier_hmac,identifier_key_version,password_secret,totp_secret) VALUES($1,'mother@fixture.test',decode(repeat('31',32),'hex'),1,'pw','totp')`, []any{mother}},
		{`INSERT INTO tsw_workspaces(id,platform_workspace_id,display_name) VALUES($1,$2,'target')`, []any{space, space.String()}},
		{`INSERT INTO tsw_mother_personal_sessions(mother_account_id,secret_revision,generation,key_version,nonce,sealed_session,expires_at) VALUES($1,1,$2,1,decode(repeat('11',12),'hex'),decode(repeat('22',32),'hex'),now()+interval '1 day')`, []any{mother, generation}},
		{`INSERT INTO tsw_mother_discoveries(mother_account_id,run_id,secret_revision,session_generation,status) VALUES($1,$2,1,$3,'discovered')`, []any{mother, run, generation}},
		{`INSERT INTO tsw_mother_workspace_visibility(mother_account_id,workspace_id,run_id,access_status) VALUES($1,$2,$3,'readable')`, []any{mother, space, run}},
		{`INSERT INTO tsw_selected_workspace_tokens(mother_account_id,workspace_id,discovery_run_id,session_generation,secret_revision,exchange_id,status,key_version,nonce,sealed_access,expires_at) VALUES($1,$2,$3,$4,1,$5,'ready',$6,$7,$8,$9)`, []any{mother, space, run, generation, exchange, key, nonce, sealed, access.ExpiresAt}},
		{`INSERT INTO tsw_workspace_verifications(workspace_id,mother_account_id,discovery_run_id,session_generation,secret_revision,token_attempt,token_exchange_id,source,outcome,permission,completeness,observed_at,expires_at,active_until,seat_limit,member_count,pending_invite_count,seat_type_counts,seat_entitlements) VALUES($1,$2,$3,$4,1,1,$5,'injected_platform_reader','verified','read','complete',now(),now()+interval '1 day',now()+interval '30 days',20,1,0,'{"default":1,"prolite":0}','{"default":1,"prolite":200}')`, []any{space, mother, run, generation, exchange}},
		{`INSERT INTO tsw_standby_child_batches(id,name) VALUES($1,'first')`, []any{batch}},
		{`INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,$2,$3,1,$2)`, []any{child, "one@fixture.test", bytes.Repeat([]byte{0x47}, 32)}},
		{`INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,$2,$3,1,$2)`, []any{other, "two@fixture.test", bytes.Repeat([]byte{0x48}, 32)}},
		{`INSERT INTO tsw_target_credentials(target_account_id,password_secret,totp_secret,material_status,materials_sealed) VALUES($1,$2,$3,'complete',true)`, []any{child, sealedPassword, sealedTotp}},
		{`INSERT INTO tsw_target_credentials(target_account_id,password_secret,material_status,materials_sealed) VALUES($1,$2,'needs_totp',true)`, []any{other, sealedPassword}},
		{`INSERT INTO tsw_standby_child_memberships(target_account_id,batch_id) VALUES($1,$2)`, []any{child, batch}},
	}
	for _, s := range statements {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("fixture: %v\n%s", err, s.sql)
		}
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("fixture write: %v", err)
		}
	}
	binding := uuid.New()
	exec(`INSERT INTO tsw_mother_workspace_bindings(id,mother_account_id,workspace_id) VALUES($1,$2,$3)`, binding, mother, space)
	exec(`INSERT INTO tsw_workspace_verification_entries(verification_id,kind,identifier,identifier_hmac,identifier_key_version,status,role,platform_member_id) SELECT id,'member','mother@fixture.test',decode(repeat('32',32),'hex'),1,'listed','account-owner','owner-member' FROM tsw_workspace_verifications WHERE mother_account_id=$1 AND workspace_id=$2`, mother, space)
	get := func() ownerapi.OperationDraft {
		return draftResult(t, operationDraftRequest(t, h, session, csrf, "GET", nil), 200)
	}
	d := draftResult(t, operationDraftRequest(t, h, session, csrf, "POST", nil), 200)
	choose := func(body map[string]any) {
		t.Helper()
		body["expectedVersion"] = d.Version
		d = draftResult(t, operationDraftRequest(t, h, session, csrf, "PATCH", body), 200)
	}
	choose(map[string]any{"choice": "mother", "motherAccountId": mother})
	choose(map[string]any{"choice": "workspace", "workspaceId": space})
	choose(map[string]any{"choice": "children", "batchId": batch, "batchVersion": 1, "children": []map[string]any{{"accountId": child, "membershipVersion": 1}}})
	request := func(method, path string, body any, handle func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Origin", "https://owner.test")
		r.Header.Set(auth.CSRFHeaderName, csrf)
		r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
		w := httptest.NewRecorder()
		handle(w, r)
		return w
	}
	plan := map[string]any{"bindingId": binding, "expectedVersion": d.Version, "plannedAt": time.Now().Add(24 * time.Hour)}
	prepare := func() *httptest.ResponseRecorder {
		return request("POST", "/api/owner/v1/operation-draft/prepare", plan, func(w http.ResponseWriter, r *http.Request) {
			h.PrepareOperationDraft(w, r, ownerapi.PrepareOperationDraftParams{})
		})
	}
	// Each observed role is a new immutable verification snapshot. Preparation
	// checks the matching mother, not another member's owner role.
	observeRole := func(role, identifier string) {
		t.Helper()
		var verification int64
		err := pool.QueryRow(ctx, `INSERT INTO tsw_workspace_verifications(workspace_id,mother_account_id,discovery_run_id,session_generation,secret_revision,token_attempt,token_exchange_id,source,outcome,permission,completeness,observed_at,expires_at,active_until,seat_limit,member_count,pending_invite_count,seat_type_counts,seat_entitlements)
		 SELECT workspace_id,mother_account_id,discovery_run_id,session_generation,secret_revision,token_attempt,token_exchange_id,source,'verified','read','complete',now(),now()+interval '1 day',active_until,seat_limit,1,0,seat_type_counts,seat_entitlements FROM tsw_workspace_verifications WHERE mother_account_id=$1 AND workspace_id=$2 ORDER BY id DESC LIMIT 1 RETURNING id`, mother, space).Scan(&verification)
		if err != nil {
			t.Fatal(err)
		}
		var storedRole any = role
		if role == "" {
			storedRole = nil
		}
		exec(`INSERT INTO tsw_workspace_verification_entries(verification_id,kind,identifier,identifier_hmac,identifier_key_version,status,role,platform_member_id) VALUES($1,'member',$2,decode(repeat('32',32),'hex'),1,'listed',$3,'owner-member')`, verification, identifier, storedRole)
		exec(`UPDATE tsw_operation_selection_drafts SET verification_id=$1 WHERE owner_id=$2`, verification, ownerID)
	}
	for _, role := range []string{"standard-user", "unknown", "", "unexpected-role"} {
		observeRole(role, "mother@fixture.test")
		if response := prepare(); response.Code != 409 || !bytes.Contains(response.Body.Bytes(), []byte("workspace_not_manageable")) {
			t.Fatalf("role %q admitted preparation: %d %s", role, response.Code, response.Body.String())
		}
	}
	observeRole("account-owner", "other-mother@fixture.test")
	if response := prepare(); response.Code != 409 {
		t.Fatalf("another mother's owner role admitted: %d", response.Code)
	}
	observeRole("account-owner", "mother@fixture.test")
	first := prepare()
	if first.Code != 200 {
		t.Fatalf("prepare %d: %s", first.Code, first.Body.String())
	}
	var prepared ownerapi.Batch
	if err = json.Unmarshal(first.Body.Bytes(), &prepared); err != nil {
		t.Fatal(err)
	}
	d = get()
	if d.ExecutionBatchId == nil || *d.ExecutionBatchId != prepared.Id || prepared.SourceBatchName == nil || *prepared.SourceBatchName != "first" {
		t.Fatalf("server did not save run association: %+v %+v", d, prepared)
	}
	repeat := prepare()
	if repeat.Code != 200 {
		t.Fatalf("lost response recovery: %d %s", repeat.Code, repeat.Body.String())
	}
	var recovered ownerapi.Batch
	json.Unmarshal(repeat.Body.Bytes(), &recovered)
	if recovered.Id != prepared.Id {
		t.Fatal("retry created a second execution")
	}
	var count int
	pool.QueryRow(ctx, `SELECT count(*) FROM tsw_batches WHERE source_standby_batch_id=$1`, batch).Scan(&count)
	if count != 1 {
		t.Fatalf("plan count=%d", count)
	}
	plan["expectedVersion"] = d.Version
	plan["plannedAt"] = time.Now().Add(25 * time.Hour)
	observeRole("administrator", "mother@fixture.test")
	updated := prepare()
	if updated.Code != 200 {
		t.Fatalf("editable plan: %d %s", updated.Code, updated.Body.String())
	}
	json.Unmarshal(updated.Body.Bytes(), &recovered)
	if recovered.Id != prepared.Id {
		t.Fatal("editing detached the current plan")
	}
	d = get()
	loginBody := map[string]any{"idempotencyKey": "integration-login-first"}
	login := func() *httptest.ResponseRecorder {
		return request("POST", "/api/owner/v1/batches/"+prepared.Id.String()+"/deliveries/login", loginBody, func(w http.ResponseWriter, r *http.Request) {
			h.StartBatchAccountLogin(w, r, prepared.Id, ownerapi.StartBatchAccountLoginParams{})
		})
	}
	if response := login(); response.Code != 409 {
		t.Fatalf("early login was accepted: %d %s", response.Code, response.Body.String())
	}
	operation, target, joinTask, lease := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec(`UPDATE tsw_batches SET status='joining',version=version+1 WHERE id=$1`, prepared.Id)
	exec(`INSERT INTO tsw_operations(id,owner_id,workspace_id,batch_id,operation_type,idempotency_key,request_hash,input_snapshot,correlation_id) VALUES($1,$2,$3,$4,'join','operation-page-join',decode(repeat('11',32),'hex'),'{}','operation-page')`, operation, ownerID, space, prepared.Id)
	exec(`INSERT INTO tsw_operation_targets(id,operation_id,target_account_id,ordinal) VALUES($1,$2,$3,1)`, target, operation, child)
	exec(`INSERT INTO tsw_tasks(id,workspace_id,target_account_id,operation_target_id,task_type,dedupe_key,input_snapshot,correlation_id,status,lease_owner,lease_token,lease_expires_at,attempt_count) VALUES($1,$2,$3,$4,'join','operation-page-join','{}','operation-page','running','fixture',$5,now()+interval '10 minutes',1)`, joinTask, space, child, target, lease)
	store := task.NewStore(pool, h.keyRing)
	h.workspaceTasks = store
	item := task.Task{ID: joinTask.String(), TaskType: "join", WorkspaceID: space.String(), TargetAccountID: child.String(), OperationTargetID: target.String(), LeaseToken: lease, AttemptNo: 1, CorrelationID: "operation-page"}
	if err = store.FinishJoin(ctx, item, task.JoinTarget{OperationID: operation.String(), BatchID: prepared.Id.String(), WorkspaceID: space.String(), TargetID: child.String()}, "succeeded", "member_confirmed", "", "membership_verify", &platform.MembershipResult{Present: true, Complete: true, PlatformMemberID: "fixture-member", SeatType: "prolite"}); err != nil {
		t.Fatal(err)
	}
	pool.QueryRow(ctx, `SELECT count(*) FROM tsw_tasks WHERE task_type='oauth_generate'`).Scan(&count)
	if count != 0 {
		t.Fatalf("invitation started %d unauthorized logins", count)
	}
	exec(`UPDATE tsw_tasks SET status='succeeded',lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,finished_at=now() WHERE id=$1`, joinTask)
	// A server mutation cannot change objects once invitation has begun.
	response := operationDraftRequest(t, h, session, csrf, "PATCH", map[string]any{"choice": "mother", "motherAccountId": mother, "expectedVersion": d.Version})
	if response.Code != 409 {
		t.Fatalf("running objects changed: %d %s", response.Code, response.Body.String())
	}
	var member uuid.UUID
	pool.QueryRow(ctx, `SELECT id FROM tsw_batch_memberships WHERE batch_id=$1`, prepared.Id).Scan(&member)
	loginBody["targetAccountId"] = uuid.New()
	if response := login(); response.Code != 422 {
		t.Fatalf("foreign membership accepted: %d", response.Code)
	}
	delete(loginBody, "targetAccountId")
	response = login()
	if response.Code != 202 {
		t.Fatalf("login %d %s", response.Code, response.Body.String())
	}
	var queued ownerapi.ProbeDeliveryResponse
	json.Unmarshal(response.Body.Bytes(), &queued)
	if queued.Queued != 1 {
		t.Fatalf("queued=%d", queued.Queued)
	}
	response = login()
	if response.Code != 202 {
		t.Fatalf("login replay %d %s", response.Code, response.Body.String())
	}
	json.Unmarshal(response.Body.Bytes(), &queued)
	if queued.Queued != 0 {
		t.Fatal("duplicate login was queued")
	}
	// Worker admission independently requires explicit login authorization.
	exec(`UPDATE tsw_batches SET login_started_at=NULL WHERE id=$1`, prepared.Id)
	if _, err = store.NextNetworkTaskType(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("scheduler admitted unauthorized login: %v", err)
	}
	if _, err = store.ClaimDelivery(ctx, "fixture", time.Minute, "oauth_generate", task.AttemptRoute{Mode: "direct"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("worker claimed unauthorized login: %v", err)
	}
	exec(`UPDATE tsw_batches SET login_started_at=now() WHERE id=$1`, prepared.Id)
	claimed, err := store.ClaimDelivery(ctx, "fixture", time.Minute, "oauth_generate", task.AttemptRoute{Mode: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	if claimed.MembershipID != member.String() {
		t.Fatalf("wrong membership: %+v", claimed)
	}
	// Explicit failure retry uses a new task without replaying successful accounts.
	exec(`UPDATE tsw_tasks SET status='failed',lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,finished_at=now() WHERE id=$1`, claimed.ID)
	exec(`UPDATE tsw_oauth_assets SET status='unavailable',unavailable_reason='oauth_generation_failed',version=version+1 WHERE membership_id=$1`, member)
	loginBody["idempotencyKey"] = "integration-login-retry"
	loginBody["targetAccountId"] = child
	response = login()
	if response.Code != 202 {
		t.Fatalf("retry %d %s", response.Code, response.Body.String())
	}
	json.Unmarshal(response.Body.Bytes(), &queued)
	if queued.Queued != 1 {
		t.Fatal("failure did not get a new login attempt")
	}
	// Real multi-page membership facts stay complete while each response is bounded.
	for i := 2; i <= 125; i++ {
		account, operationTarget, membership := uuid.New(), uuid.New(), uuid.New()
		fingerprint := sha256.Sum256(account[:])
		exec(`INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,$2,$3,1,$2)`, account, fmt.Sprintf("page-%d@fixture.test", i), fingerprint[:])
		exec(`INSERT INTO tsw_operation_targets(id,operation_id,target_account_id,ordinal,status,completed_at) VALUES($1,$2,$3,$4,'succeeded',now())`, operationTarget, operation, account, i)
		exec(`INSERT INTO tsw_batch_memberships(id,batch_id,target_account_id,join_operation_target_id,joined_at) VALUES($1,$2,$3,$4,now())`, membership, prepared.Id, account, operationTarget)
		exec(`INSERT INTO tsw_oauth_assets(membership_id) VALUES($1)`, membership)
	}
	// Operation pages carry the account identity for their own frozen ordering;
	// they never depend on a differently sorted batch or membership page.
	for _, size := range []int{20, 50, 100} {
		seen := map[uuid.UUID]bool{}
		for page := 1; page <= (125+size-1)/size; page++ {
			result, err := h.joinOperationByBatch(httptest.NewRequest("GET", "/operation", nil), prepared.Id.String(), page, size)
			if err != nil || result.TargetTotal != 125 || len(result.Targets) != min(size, 125-(page-1)*size) {
				t.Fatalf("operation page: %+v %v", result, err)
			}
			for _, target := range result.Targets {
				if target.Identifier == "" || seen[target.Id] {
					t.Fatalf("operation identity lost/duplicated: %+v", target)
				}
				seen[target.Id] = true
			}
		}
		if len(seen) != 125 {
			t.Fatalf("operation scope incomplete: %d", len(seen))
		}
	}
	// Current-batch delivery pagination and counts are independent of empty pages.
	for _, size := range []int{20, 50, 100} {
		seen := map[uuid.UUID]bool{}
		for page := 1; page <= (125+size-1)/size+1; page++ {
			p, s := ownerapi.Page(page), ownerapi.PageSize(size)
			response = request("GET", "/api/owner/v1/batches/"+prepared.Id.String()+"/deliveries", nil, func(w http.ResponseWriter, r *http.Request) {
				r.SetPathValue("batchId", prepared.Id.String())
				h.GetBatchDeliveries(w, r, prepared.Id, ownerapi.GetBatchDeliveriesParams{Page: &p, PageSize: &s})
			})
			if response.Code != 200 {
				t.Fatalf("delivery page %d %s", response.Code, response.Body.String())
			}
			var result ownerapi.DeliveryList
			json.Unmarshal(response.Body.Bytes(), &result)
			expected := max(0, min(size, 125-(page-1)*size))
			if result.Total != 125 || result.PendingCount != 125 || result.PageSize != size || len(result.Items) != expected {
				t.Fatalf("paged facts: %+v", result)
			}
			for _, item := range result.Items {
				if seen[item.MembershipId] || item.Identifier == "" {
					t.Fatalf("duplicated or unidentified member: %+v", item)
				}
				seen[item.MembershipId] = true
			}
		}
		if len(seen) != 125 {
			t.Fatalf("page size %d lost members: %d", size, len(seen))
		}
	}
	// The result link scopes card records by the immutable execution ID.
	for _, scope := range []uuid.UUID{prepared.Id, uuid.New()} {
		response = request("GET", "/api/owner/v1/deliveries", nil, func(w http.ResponseWriter, r *http.Request) {
			h.ListDeliveryRecords(w, r, ownerapi.ListDeliveryRecordsParams{BatchId: &scope})
		})
		if response.Code != 200 {
			t.Fatalf("scoped card list %d %s", response.Code, response.Body.String())
		}
		var result ownerapi.DeliveryRecordList
		json.Unmarshal(response.Body.Bytes(), &result)
		expected := 0
		if scope == prepared.Id {
			expected = 125
		}
		if result.Total != expected || len(result.Items) != min(expected, 20) {
			t.Fatalf("scoped card facts: %+v", result)
		}
	}
	// Failed invitations can retry, while successful and uncertain effects stay intact.
	exec(`UPDATE tsw_batches SET status='joining' WHERE id=$1`, prepared.Id)
	exec(`UPDATE tsw_operations SET status='failed',completed_at=now() WHERE id=$1`, operation)
	failedTarget, unknownTarget := uuid.New(), uuid.New()
	exec(`INSERT INTO tsw_operation_targets(id,operation_id,target_account_id,ordinal,status,completed_at) VALUES($1,$2,$3,126,'failed',now())`, failedTarget, operation, other)
	uncertainAccount := uuid.New()
	exec(`INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,'unknown@fixture.test',$2,1,'unknown')`, uncertainAccount, bytes.Repeat([]byte{0xfe}, 32))
	exec(`INSERT INTO tsw_operation_targets(id,operation_id,target_account_id,ordinal,status,completed_at,platform_request_may_have_reached,platform_request_stage,platform_request_started_at) VALUES($1,$2,$3,127,'unknown',now(),true,'request_join',now())`, unknownTarget, operation, uncertainAccount)
	for i := 0; i < 2; i++ {
		response = request("POST", "/api/owner/v1/batches/"+prepared.Id.String()+"/join-retry", map[string]any{"idempotencyKey": "invitation-retry-safe"}, func(w http.ResponseWriter, r *http.Request) {
			h.RetryBatchInvitation(w, r, prepared.Id, ownerapi.RetryBatchInvitationParams{})
		})
		if response.Code != 202 {
			t.Fatalf("invitation retry %d %s", response.Code, response.Body.String())
		}
	}
	pool.QueryRow(ctx, `SELECT count(*) FROM tsw_tasks WHERE task_type='join' AND operation_target_id=$1`, failedTarget).Scan(&count)
	if count != 1 {
		t.Fatalf("failed invitation retry count=%d", count)
	}
	pool.QueryRow(ctx, `SELECT count(*) FROM tsw_tasks WHERE task_type='join' AND operation_target_id=$1`, unknownTarget).Scan(&count)
	if count != 0 {
		t.Fatal("uncertain invitation was replayed")
	}
	response = request("POST", "/api/owner/v1/batches/"+prepared.Id.String()+"/join-reconcile", map[string]any{"idempotencyKey": "invitation-reconcile-safe"}, func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("batchId", prepared.Id.String())
		h.createJoinReconciliation(w, r)
	})
	if response.Code != 202 {
		t.Fatalf("reconcile %d %s", response.Code, response.Body.String())
	}
	pool.QueryRow(ctx, `SELECT count(*) FROM tsw_tasks WHERE task_type='join_reconcile' AND operation_target_id=$1`, unknownTarget).Scan(&count)
	if count != 1 {
		t.Fatal("uncertain invitation did not queue one verification")
	}
	var invitation ownerapi.JoinOperation
	json.Unmarshal(response.Body.Bytes(), &invitation)
	if invitation.Status != "queued" || invitation.SucceededCount != 125 {
		t.Fatalf("reconciliation lost completed facts: %+v", invitation)
	}
	// A new selection never removes real execution facts.
	response = request("DELETE", "/api/owner/v1/operation-draft", nil, func(w http.ResponseWriter, r *http.Request) {
		h.ResetOperationDraft(w, r, ownerapi.ResetOperationDraftParams{})
	})
	fresh := draftResult(t, response, 200)
	if fresh.ExecutionBatchId != nil || fresh.MotherAccountId != nil || len(fresh.Children) != 0 {
		t.Fatal("new selection retained old choices")
	}
	pool.QueryRow(ctx, `SELECT count(*) FROM tsw_batch_memberships WHERE batch_id=$1`, prepared.Id).Scan(&count)
	if count != 125 {
		t.Fatal("new selection removed real members")
	}
	p, s := ownerapi.Page(10), ownerapi.PageSize(20)
	only := true
	response = request("GET", "/api/owner/v1/batches", nil, func(w http.ResponseWriter, r *http.Request) {
		h.ListBatches(w, r, ownerapi.ListBatchesParams{Page: &p, PageSize: &s, OperationOnly: &only})
	})
	if response.Code != 200 {
		t.Fatalf("history %d %s", response.Code, response.Body.String())
	}
	var history ownerapi.BatchList
	json.Unmarshal(response.Body.Bytes(), &history)
	if history.Total != 1 || len(history.Items) != 0 {
		t.Fatalf("history count %+v", history)
	}
}
