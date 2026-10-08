//go:build integration

package runtime

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

func operationTargetLoginFixture(t *testing.T) (premiumFixture, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	f := premiumOperationFixture(t)
	op, first, second, other := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.h.pool.Exec(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE tsw_batches SET status='joining' WHERE id=$1`, f.batch)
	exec(`INSERT INTO tsw_operations(id,owner_id,workspace_id,batch_id,operation_type,idempotency_key,request_hash,input_snapshot,correlation_id) SELECT $1,id,$2,$3,'join','target-login-fixture',decode(repeat('11',32),'hex'),'{}','target-login' FROM tsw_owners LIMIT 1`, op, f.workspace, f.batch)
	exec(`INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,'login-second@example.test',decode(repeat('c2',32),'hex'),1,'second')`, other)
	exec(`INSERT INTO tsw_target_credentials(target_account_id,password_secret,totp_secret,material_status,materials_sealed) SELECT $1,password_secret,totp_secret,material_status,materials_sealed FROM tsw_target_credentials WHERE target_account_id=$2`, other, f.target)
	exec(`INSERT INTO tsw_batch_targets(batch_id,target_account_id,ordinal) VALUES($1,$2,2)`, f.batch, other)
	exec(`INSERT INTO tsw_operation_targets(id,operation_id,target_account_id,ordinal,status,invitation_confirmed_at,diagnostic_code,completed_at) VALUES($1,$2,$3,1,'failed',now(),'oauth_generation_failed',now()),($4,$2,$5,2,'invited',now(),NULL,NULL)`, first, op, f.target, second, other)
	return f, first, second, other
}

func operationLoginRequest(t *testing.T, f premiumFixture, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/login", bytes.NewReader(raw))
	r.Header.Set("Origin", "https://owner.test")
	r.Header.Set(auth.CSRFHeaderName, f.csrf)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: f.session})
	r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: f.csrf})
	w := httptest.NewRecorder()
	f.h.StartBatchAccountLogin(w, r, f.batch, ownerapi.StartBatchAccountLoginParams{})
	return w
}

func TestOperationSingleTargetOAuthBeforeMembershipIntegration(t *testing.T) {
	f, first, second, _ := operationTargetLoginFixture(t)
	for attempt := 0; attempt < 2; attempt++ {
		w := operationLoginRequest(t, f, map[string]any{"idempotencyKey": "single-account-login", "targetAccountId": f.target})
		var result ownerapi.ProbeDeliveryResponse
		if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Queued != 1-attempt {
			t.Fatalf("single login/replay: %d %s", w.Code, w.Body.String())
		}
	}
	var firstCount, secondCount int
	if err := f.h.pool.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE operation_target_id=$1),count(*) FILTER(WHERE operation_target_id=$2) FROM tsw_tasks WHERE task_type='join'`, first, second).Scan(&firstCount, &secondCount); err != nil {
		t.Fatal(err)
	}
	if firstCount != 1 || secondCount != 0 {
		t.Fatalf("single retry changed other account: first=%d second=%d", firstCount, secondCount)
	}
	for _, body := range []map[string]any{
		{"idempotencyKey": "foreign-account-login", "targetAccountId": uuid.New()},
		{"idempotencyKey": "obsolete-login-shape", "membershipId": uuid.New()},
	} {
		if w := operationLoginRequest(t, f, body); w.Code != 422 {
			t.Fatalf("invalid scope accepted: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestOperationSingleTargetOAuthAfterMembershipIntegration(t *testing.T) {
	f, _, second, other := operationTargetLoginFixture(t)
	membership := uuid.New()
	if _, err := f.h.pool.Exec(t.Context(), `INSERT INTO tsw_batch_memberships(id,batch_id,target_account_id,join_operation_target_id,joined_at,seat_type) VALUES($1,$2,$3,$4,now(),'prolite')`, membership, f.batch, other, second); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.pool.Exec(t.Context(), `UPDATE tsw_operation_targets SET status='succeeded',target_account_id=NULL,membership_id=$1,completed_at=now() WHERE id=$2`, membership, second); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.pool.Exec(t.Context(), `INSERT INTO tsw_oauth_assets(membership_id,status,unavailable_reason) VALUES($1,'unavailable','oauth_generation_failed')`, membership); err != nil {
		t.Fatal(err)
	}
	w := operationLoginRequest(t, f, map[string]any{"idempotencyKey": "single-member-login", "targetAccountId": other})
	var result ownerapi.ProbeDeliveryResponse
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Queued != 1 {
		t.Fatalf("member retry: %d %s", w.Code, w.Body.String())
	}
	var oauth, join int
	if err := f.h.pool.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE task_type='oauth_generate' AND membership_id=$1),count(*) FILTER(WHERE task_type='join') FROM tsw_tasks`, membership).Scan(&oauth, &join); err != nil {
		t.Fatal(err)
	}
	if oauth != 1 || join != 0 {
		t.Fatalf("wrong login scope: oauth=%d join=%d", oauth, join)
	}
}

func TestOperationRecordsUseActualExecutionFactsIntegration(t *testing.T) {
	f, first, _, _ := operationTargetLoginFixture(t)
	if _, err := f.h.pool.Exec(t.Context(), `UPDATE tsw_operation_targets SET invitation_confirmed_at=NULL,status='failed',diagnostic_code='invitation_rejected',platform_request_may_have_reached=true,platform_request_stage='send_invitation',platform_request_started_at=now() WHERE id=$1`, first); err != nil {
		t.Fatal(err)
	}
	batch, err := f.h.batchByID(httptest.NewRequest("GET", "/batch", nil), f.batch.String())
	if err != nil {
		t.Fatal(err)
	}
	if batch.Status != "joining" || batch.Execution.ActiveTaskCount != 0 || batch.Execution.InvitationFailedCount != 1 || batch.Execution.InvitationUncertainCount != 0 || batch.Execution.InvitationConfirmedCount != 1 {
		t.Fatalf("wrong stopped invitation facts: %+v", batch.Execution)
	}
	if _, err := f.h.pool.Exec(t.Context(), `UPDATE tsw_operation_targets SET status='unknown',platform_request_may_have_reached=true,platform_request_stage='send_invitation',platform_request_started_at=now() WHERE id=$1`, first); err != nil {
		t.Fatal(err)
	}
	batch, err = f.h.batchByID(httptest.NewRequest("GET", "/batch", nil), f.batch.String())
	if err != nil || batch.Execution.InvitationFailedCount != 0 || batch.Execution.InvitationUncertainCount != 1 {
		t.Fatalf("uncertain and unsent conflated: %+v %v", batch.Execution, err)
	}
	operation, err := f.h.joinOperationByBatch(httptest.NewRequest("GET", "/operation", nil), f.batch.String(), 1, 20)
	if err != nil || operation.RetryableInvitationCount != 0 {
		t.Fatalf("confirmed or uncertain invitation became replayable: %+v %v", operation, err)
	}
}
