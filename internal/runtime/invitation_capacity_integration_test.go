//go:build integration

package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

func TestPremiumSeatLimitBlocksFullSpace(t *testing.T) {
	f := premiumOperationFixtureWithCapacity(t, 9, 9, 0)
	preview, err := f.h.joinPreview(httptest.NewRequest("GET", "/preview", nil), f.batch.String())
	if err != nil {
		t.Fatal(err)
	}
	if preview.CanProceed {
		t.Fatal("9 paid premium seats already occupied by 9 members must reject a new invitation")
	}
	w := seatInvitationRequest(t, f, "zero-seats-no-new-invite")
	if w.Code != 409 {
		t.Fatalf("full capacity must reject only sending: %d %s", w.Code, w.Body.String())
	}
	var status string
	var tasks int
	f.h.pool.QueryRow(t.Context(), `SELECT status FROM tsw_batches WHERE id=$1`, f.batch).Scan(&status)
	f.h.pool.QueryRow(t.Context(), `SELECT count(*) FROM tsw_tasks WHERE workspace_id=$1`, f.workspace).Scan(&tasks)
	if status != "planned" || tasks != 0 {
		t.Fatalf("zero seats must preserve editable plan, status=%s tasks=%d", status, tasks)
	}
	for _, blocker := range preview.Blockers {
		if blocker.Code == "premium_capacity_exceeded" {
			return
		}
	}
	t.Fatalf("missing seat-capacity explanation: %+v", preview.Blockers)
}

func seatInvitationRequest(t *testing.T, f premiumFixture, key string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"idempotencyKey": key, "confirm": true})
	r := httptest.NewRequest("POST", "/join", bytes.NewReader(body))
	r.SetPathValue("batchId", f.batch.String())
	r.Header.Set("Origin", "https://owner.test")
	r.Header.Set(auth.CSRFHeaderName, f.csrf)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: f.session})
	r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: f.csrf})
	w := httptest.NewRecorder()
	f.h.createJoinOperation(w, r)
	return w
}

func seatExec(t *testing.T, f premiumFixture, sql string, args ...any) {
	t.Helper()
	if _, err := f.h.pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func TestPremiumSeatLimitTwentyAccountsKeepWaitingAndContinueOAuth(t *testing.T) {
	f := premiumOperationFixtureWithCapacity(t, 9, 0, 0)
	var waitingAccount uuid.UUID
	for ordinal := 2; ordinal <= 20; ordinal++ {
		account := uuid.New()
		if ordinal == 10 {
			waitingAccount = account
		}
		seatExec(t, f, `INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,$2,digest($2,'sha256'),1,$2)`, account, fmt.Sprintf("seat-%02d@example.test", ordinal))
		seatExec(t, f, `INSERT INTO tsw_target_credentials(target_account_id,password_secret,totp_secret,material_status,materials_sealed) SELECT $1,password_secret,totp_secret,material_status,materials_sealed FROM tsw_target_credentials WHERE target_account_id=$2`, account, f.target)
		seatExec(t, f, `INSERT INTO tsw_batch_targets(batch_id,target_account_id,ordinal) VALUES($1,$2,$3)`, f.batch, account, ordinal)
	}
	preview, err := f.h.joinPreview(httptest.NewRequest("GET", "/preview", nil), f.batch.String())
	if err != nil || !preview.CanProceed || preview.PlannedInvitationCount == nil || *preview.PlannedInvitationCount != 9 || preview.WaitingSeatCount == nil || *preview.WaitingSeatCount != 11 {
		t.Fatalf("20 accounts should use 9 seats: %+v %v", preview, err)
	}
	w := seatInvitationRequest(t, f, "twenty-accounts-nine-seats")
	if w.Code != 202 {
		t.Fatalf("partial invitation rejected: %d %s", w.Code, w.Body.String())
	}
	var operation ownerapi.JoinOperation
	if err = json.Unmarshal(w.Body.Bytes(), &operation); err != nil {
		t.Fatal(err)
	}
	if operation.TargetTotal != 20 || operation.WaitingSeatCount != 11 || operation.ActiveTaskCount != 9 || operation.BlockedCount != 0 {
		t.Fatalf("lost waiting accounts or wrong task count: %+v", operation)
	}
	var wrong int
	f.h.pool.QueryRow(t.Context(), `SELECT count(*) FROM tsw_operation_targets WHERE operation_id=$1 AND ((ordinal<=9 AND status<>'queued') OR (ordinal>9 AND (diagnostic_code<>'premium_capacity_exceeded' OR platform_request_may_have_reached)))`, operation.Id).Scan(&wrong)
	if wrong != 0 {
		t.Fatal("invitations must use frozen account order and keep later accounts unsent")
	}
	w = seatInvitationRequest(t, f, "twenty-accounts-nine-seats")
	if w.Code != 202 {
		t.Fatalf("original request should restore its records: %d %s", w.Code, w.Body.String())
	}
	seatExec(t, f, `UPDATE tsw_tasks SET status='succeeded',finished_at=now() WHERE operation_target_id IN(SELECT id FROM tsw_operation_targets WHERE operation_id=$1)`, operation.Id)
	seatExec(t, f, `UPDATE tsw_operation_targets SET status='invited',invitation_confirmed_at=now() WHERE operation_id=$1 AND ordinal<=9`, operation.Id)
	login := operationLoginRequest(t, f, map[string]any{"idempotencyKey": "nine-confirmed-oauth"})
	if login.Code != 202 {
		t.Fatalf("11 waiting accounts must not block 9 confirmed invitations: %d %s", login.Code, login.Body.String())
	}
	var queued ownerapi.ProbeDeliveryResponse
	json.Unmarshal(login.Body.Bytes(), &queued)
	if queued.Queued != 9 {
		t.Fatalf("OAuth must queue only 9 confirmed targets: %+v", queued)
	}
	if res := operationLoginRequest(t, f, map[string]any{"idempotencyKey": "waiting-account-no-oauth", "targetAccountId": waitingAccount}); res.Code != 422 {
		t.Fatalf("uninvited account must not start OAuth: %d %s", res.Code, res.Body.String())
	}
	// A later sync explicitly opens nine additional paid seats. The first nine
	// invitations remain reserved, and only nine of the eleven waiting targets run.
	seatExec(t, f, `INSERT INTO tsw_workspace_verifications(workspace_id,mother_account_id,discovery_run_id,session_generation,secret_revision,token_attempt,token_exchange_id,source,outcome,permission,completeness,observed_at,expires_at,active_until,seat_limit,member_count,pending_invite_count,seat_type_counts,seat_entitlements) SELECT workspace_id,mother_account_id,discovery_run_id,session_generation,secret_revision,token_attempt,token_exchange_id,source,outcome,permission,completeness,now(),expires_at,active_until,seat_limit,member_count,9,seat_type_counts,'{"default":2,"prolite":18}' FROM tsw_workspace_verifications WHERE id=$1`, f.verification)
	var next int64
	f.h.pool.QueryRow(t.Context(), `SELECT max(id) FROM tsw_workspace_verifications WHERE workspace_id=$1`, f.workspace).Scan(&next)
	seatExec(t, f, `INSERT INTO tsw_workspace_verification_entries(verification_id,kind,identifier,identifier_hmac,identifier_key_version,status,role,platform_member_id,seat_type) SELECT $1,kind,identifier,identifier_hmac,identifier_key_version,status,role,platform_member_id,seat_type FROM tsw_workspace_verification_entries WHERE verification_id=$2`, next, f.verification)
	seatExec(t, f, `INSERT INTO tsw_workspace_verification_entries(verification_id,kind,identifier,identifier_hmac,identifier_key_version,status,seat_type) SELECT $1,'pending_invite',account.identifier,account.identifier_hmac,account.identifier_key_version,'pending','prolite' FROM tsw_operation_targets target JOIN tsw_target_accounts account ON account.id=target.target_account_id WHERE target.operation_id=$2 AND target.ordinal<=9`, next, operation.Id)

	r := httptest.NewRequest("POST", "/retry", bytes.NewBufferString(`{"idempotencyKey":"continue-waiting-seats"}`))
	r.Header.Set("Origin", "https://owner.test")
	r.Header.Set(auth.CSRFHeaderName, f.csrf)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: f.session})
	r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: f.csrf})
	res := httptest.NewRecorder()
	f.h.RetryBatchInvitation(res, r, f.batch, ownerapi.RetryBatchInvitationParams{})
	if res.Code != 202 {
		t.Fatalf("resume failed: %d %s", res.Code, res.Body.String())
	}
	json.Unmarshal(res.Body.Bytes(), &operation)
	if operation.TargetTotal != 20 || operation.InvitationConfirmedCount != 9 || operation.WaitingSeatCount != 2 {
		t.Fatalf("resume must preserve success and remaining targets: %+v", operation)
	}
}

func TestPremiumSeatLimitConcurrentBatchesCannotShareFinalSeat(t *testing.T) {
	f := premiumOperationFixtureWithCapacity(t, 1, 0, 0)
	other := f
	other.batch = uuid.New()
	other.target = uuid.New()
	seatExec(t, f, `INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,'other-capacity@example.test',digest('other-capacity@example.test','sha256'),1,'other')`, other.target)
	seatExec(t, f, `INSERT INTO tsw_target_credentials(target_account_id,password_secret,totp_secret,material_status,materials_sealed) SELECT $1,password_secret,totp_secret,material_status,materials_sealed FROM tsw_target_credentials WHERE target_account_id=$2`, other.target, f.target)
	seatExec(t, f, `INSERT INTO tsw_batches(id,binding_id,sequence_no,status,planned_at) SELECT $1,binding_id,2,'planned',planned_at FROM tsw_batches WHERE id=$2`, other.batch, f.batch)
	seatExec(t, f, `INSERT INTO tsw_batch_targets(batch_id,target_account_id,ordinal) VALUES($1,$2,1)`, other.batch, other.target)
	var wg sync.WaitGroup
	responses := make(chan int, 2)
	for i, item := range []premiumFixture{f, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- seatInvitationRequest(t, item, fmt.Sprintf("final-seat-batch-%d", i)).Code
		}()
	}
	wg.Wait()
	close(responses)
	accepted, rejected := 0, 0
	for code := range responses {
		if code == 202 {
			accepted++
		} else if code == 409 {
			rejected++
		} else {
			t.Fatalf("unexpected response %d", code)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("final seat allocated twice: accepted=%d rejected=%d", accepted, rejected)
	}
	var tasks int
	f.h.pool.QueryRow(t.Context(), `SELECT count(*) FROM tsw_tasks WHERE workspace_id=$1 AND task_type='join'`, f.workspace).Scan(&tasks)
	if tasks != 1 {
		t.Fatalf("expected one actual invitation task, got %d", tasks)
	}
}

func TestPremiumSeatLimitLaterCompleteSnapshotReleasesAbsentInvitation(t *testing.T) {
	f := premiumOperationFixtureWithCapacity(t, 1, 0, 0)
	w := seatInvitationRequest(t, f, "reserve-first-invitation")
	if w.Code != 202 {
		t.Fatalf("invitation failed: %d %s", w.Code, w.Body.String())
	}
	var operation ownerapi.JoinOperation
	if err := json.Unmarshal(w.Body.Bytes(), &operation); err != nil {
		t.Fatal(err)
	}
	seatExec(t, f, `UPDATE tsw_tasks SET status='succeeded',finished_at=now() WHERE operation_target_id IN(SELECT id FROM tsw_operation_targets WHERE operation_id=$1)`, operation.Id)
	seatExec(t, f, `UPDATE tsw_operation_targets SET status='invited',invitation_confirmed_at=now() WHERE operation_id=$1`, operation.Id)
	preview, err := f.h.joinPreview(httptest.NewRequest("GET", "/preview", nil), f.batch.String())
	if err != nil || preview.AvailablePremiumSeats == nil || *preview.AvailablePremiumSeats != 0 {
		t.Fatalf("confirmation newer than snapshot must reserve its seat: %+v %v", preview, err)
	}
	var next int64
	err = f.h.pool.QueryRow(t.Context(), `INSERT INTO tsw_workspace_verifications(workspace_id,mother_account_id,discovery_run_id,session_generation,secret_revision,token_attempt,token_exchange_id,source,outcome,permission,completeness,observed_at,expires_at,active_until,seat_limit,member_count,pending_invite_count,seat_type_counts,seat_entitlements) SELECT workspace_id,mother_account_id,discovery_run_id,session_generation,secret_revision,token_attempt,token_exchange_id,source,outcome,permission,completeness,now(),expires_at,active_until,seat_limit,member_count,0,seat_type_counts,seat_entitlements FROM tsw_workspace_verifications WHERE id=$1 RETURNING id`, f.verification).Scan(&next)
	if err != nil {
		t.Fatal(err)
	}
	seatExec(t, f, `INSERT INTO tsw_workspace_verification_entries(verification_id,kind,identifier,identifier_hmac,identifier_key_version,status,role,platform_member_id,seat_type) SELECT $1,kind,identifier,identifier_hmac,identifier_key_version,status,role,platform_member_id,seat_type FROM tsw_workspace_verification_entries WHERE verification_id=$2`, next, f.verification)
	preview, err = f.h.joinPreview(httptest.NewRequest("GET", "/preview", nil), f.batch.String())
	if err != nil || preview.AvailablePremiumSeats == nil || *preview.AvailablePremiumSeats != 1 {
		t.Fatalf("latest complete snapshot without that invitation must release its reservation: %+v %v", preview, err)
	}
	var confirmed int
	if err = f.h.pool.QueryRow(t.Context(), `SELECT count(*) FROM tsw_operation_targets WHERE operation_id=$1 AND invitation_confirmed_at IS NOT NULL`, operation.Id).Scan(&confirmed); err != nil || confirmed != 1 {
		t.Fatalf("releasing capacity must preserve historical invitation confirmation: %d %v", confirmed, err)
	}
}
