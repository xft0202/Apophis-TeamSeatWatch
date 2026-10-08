//go:build integration

package runtime

import (
	"bytes"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/task"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func invitationRecoveryFixture(t *testing.T, memberAlreadyPresent bool) (premiumFixture, string) {
	t.Helper()
	f := premiumOperationFixture(t)
	ctx := t.Context()
	op, tid := uuid.New(), uuid.New()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := f.h.pool.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec(`INSERT INTO tsw_operations(id,owner_id,workspace_id,batch_id,operation_type,idempotency_key,request_hash,input_snapshot,correlation_id) SELECT $1,id,$2,$3,'join','review-invitation-recovery',decode(repeat('11',32),'hex'),jsonb_build_object('seat_type','prolite','verification_id',$4::bigint),'review' FROM tsw_owners LIMIT 1`, op, f.workspace, f.batch, f.verification)
	exec(`INSERT INTO tsw_operation_targets(id,operation_id,target_account_id,ordinal) VALUES($1,$2,$3,1)`, tid, op, f.target)
	exec(`INSERT INTO tsw_tasks(operation_target_id,workspace_id,target_account_id,task_type,dedupe_key,input_snapshot,correlation_id,max_attempts) VALUES($1,$2,$3,'join','review-invite','{}','review',3)`, tid, f.workspace, f.target)
	exec(`UPDATE tsw_batches SET status='joining' WHERE id=$1`, f.batch)
	if memberAlreadyPresent {
		second := uuid.New()
		exec(`INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,'review-second@example.test',decode(repeat('c2',32),'hex'),1,'second')`, second)
		exec(`INSERT INTO tsw_target_credentials(target_account_id,password_secret,totp_secret,material_status,materials_sealed) SELECT $1,password_secret,totp_secret,material_status,materials_sealed FROM tsw_target_credentials WHERE target_account_id=$2`, second, f.target)
		exec(`INSERT INTO tsw_batch_targets(batch_id,target_account_id,ordinal) VALUES($1,$2,2)`, f.batch, second)
		exec(`INSERT INTO tsw_operation_targets(operation_id,target_account_id,ordinal,status,completed_at,diagnostic_code) VALUES($1,$2,2,'failed',now(),'credential_invalid')`, op, second)
	}
	store := task.NewStore(f.h.pool, f.h.keyRing)
	item, e := store.ClaimJoin(ctx, "review", time.Minute, task.AttemptRoute{Mode: "direct"})
	if e != nil {
		t.Fatal(e)
	}
	target, e := store.JoinTarget(ctx, item)
	if e != nil {
		t.Fatal(e)
	}
	// Same path as runJoin's authority failure, before any POST side-effect marker.
	if e = store.FinishJoin(ctx, item, target, "blocked", "workspace_authority_changed", "workspace_authority_changed", "send_invitation", nil); e != nil {
		t.Fatal(e)
	}
	reconcile, e := store.ClaimJoinReconcile(ctx, "review", time.Minute, task.AttemptRoute{Mode: "direct"})
	if e != nil {
		t.Fatal(e)
	}
	member := platform.MembershipResult{Complete: true, InvitationListComplete: true}
	if memberAlreadyPresent {
		member.Present = true
		member.SeatType = "prolite"
		member.PlatformMemberID = "review-existing-member"
	}
	if e = store.FinishJoinReconciliation(ctx, reconcile, target, member); e != nil {
		t.Fatal(e)
	}
	return f, tid.String()
}
func retryRecoveredInvitation(t *testing.T, f premiumFixture) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"idempotencyKey": "review-invitation-retry"})
	r := httptest.NewRequest("POST", "/retry", bytes.NewReader(body))
	r.Header.Set("Origin", "https://owner.test")
	r.Header.Set(auth.CSRFHeaderName, f.csrf)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: f.session})
	r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: f.csrf})
	w := httptest.NewRecorder()
	f.h.RetryBatchInvitation(w, r, f.batch, ownerapi.RetryBatchInvitationParams{})
	return w
}
func TestOperationUnsentInvitationCanResumeAfterReadOnlyAbsence(t *testing.T) {
	f, tid := invitationRecoveryFixture(t, false)
	ctx := t.Context()
	var status string
	var sent bool
	if e := f.h.pool.QueryRow(ctx, `SELECT status,platform_request_may_have_reached FROM tsw_operation_targets WHERE id=$1`, tid).Scan(&status, &sent); e != nil {
		t.Fatal(e)
	}
	w := retryRecoveredInvitation(t, f)
	var queued int
	if e := f.h.pool.QueryRow(ctx, `SELECT count(*) FROM tsw_tasks WHERE operation_target_id=$1 AND task_type='join' AND status='queued'`, tid).Scan(&queued); e != nil {
		t.Fatal(e)
	}
	if sent || w.Code != 202 || queued != 1 {
		t.Fatalf("confirmed unsent target cannot resume: status=%s requestMayHaveReached=%t HTTP=%d queued=%d", status, sent, w.Code, queued)
	}
}
func TestOperationFailedInvitationCanResumeWithExistingMember(t *testing.T) {
	f, _ := invitationRecoveryFixture(t, true)
	var batchStatus string
	if e := f.h.pool.QueryRow(t.Context(), `SELECT status FROM tsw_batches WHERE id=$1`, f.batch).Scan(&batchStatus); e != nil {
		t.Fatal(e)
	}
	w := retryRecoveredInvitation(t, f)
	if w.Code != 202 {
		t.Fatalf("pending invitations locked by unrelated existing member: batch=%s HTTP=%d body=%s", batchStatus, w.Code, w.Body.String())
	}
	var queued, members int
	if err := f.h.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM tsw_tasks WHERE workspace_id=$1 AND task_type='join' AND status='queued'),(SELECT count(*) FROM tsw_batch_memberships WHERE batch_id=$2)`, f.workspace, f.batch).Scan(&queued, &members); err != nil {
		t.Fatal(err)
	}
	if queued != 1 || members != 1 {
		t.Fatalf("retry must queue only the unsent account and retain its existing member: queued=%d members=%d", queued, members)
	}
}
