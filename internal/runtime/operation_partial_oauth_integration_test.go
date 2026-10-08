//go:build integration

package runtime

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/task"
)

func TestPartialOAuthCanDeliverAndContinueUnfinishedAccounts(t *testing.T) {
	f := newCardRevocationFixture(t)
	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Start with the same persisted state as an older partial operation: one
	// ready account, one blocked target, and an aggregate still marked joining.
	exec(`DELETE FROM tsw_cards WHERE membership_id=$1`, f.ids.membership)
	var batch, operation, firstTarget string
	if err := f.pool.QueryRow(ctx, `SELECT m.batch_id::text,t.operation_id::text,m.target_account_id::text FROM tsw_batch_memberships m JOIN tsw_operation_targets t ON t.id=m.join_operation_target_id WHERE m.id=$1`, f.ids.membership).Scan(&batch, &operation, &firstTarget); err != nil {
		t.Fatal(err)
	}
	secondTarget, secondOperationTarget := uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,'unfinished@example.com',decode(repeat('35',32),'hex'),1,'unfinished')`, secondTarget)
	exec(`INSERT INTO tsw_target_credentials(target_account_id,password_secret,totp_secret,material_status,materials_sealed) SELECT $1,password_secret,totp_secret,material_status,materials_sealed FROM tsw_target_credentials WHERE target_account_id=$2`, secondTarget, firstTarget)
	exec(`INSERT INTO tsw_batch_targets(batch_id,target_account_id,ordinal) VALUES($1,$2,1),($1,$3,2)`, batch, firstTarget, secondTarget)
	exec(`UPDATE tsw_operation_targets SET invitation_confirmed_at=now() WHERE operation_id=$1`, operation)
	exec(`INSERT INTO tsw_operation_targets(id,operation_id,target_account_id,ordinal,status,invitation_confirmed_at,diagnostic_code,completed_at) VALUES($1,$2,$3,2,'blocked',now(),'oauth_workspace_unavailable',now())`, secondOperationTarget, operation, secondTarget)
	exec(`UPDATE tsw_operations SET status='blocked',completed_at=now() WHERE id=$1`, operation)
	exec(`UPDATE tsw_batches SET status='joining',login_started_at=now() WHERE id=$1`, batch)
	secret := "TSW1-" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x72}, 20))
	saved := activateCardIntegrationRequest(t, f.owner, f.ids.membership, f.sessionToken, f.csrfToken, secret, "partial-card")
	if saved.Code != http.StatusCreated {
		t.Fatalf("ready account blocked by other target: %d %s", saved.Code, saved.Body.String())
	}
	original := decodeCardActivation(t, saved)
	login := func(key string) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"idempotencyKey": key})
		r := httptest.NewRequest("POST", "/login", bytes.NewReader(body))
		r.Header.Set("Origin", "https://owner.test")
		r.Header.Set(auth.CSRFHeaderName, f.csrfToken)
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: f.sessionToken})
		r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: f.csrfToken})
		w := httptest.NewRecorder()
		f.owner.StartBatchAccountLogin(w, r, uuid.MustParse(batch), ownerapi.StartBatchAccountLoginParams{})
		if w.Code != http.StatusAccepted {
			t.Fatalf("return to OAuth: %d %s", w.Code, w.Body.String())
		}
	}
	store := task.NewStore(f.pool, cardIntegrationKeyRing{})
	target := task.JoinTarget{OperationID: operation, BatchID: batch, WorkspaceID: f.ids.workspace, TargetID: secondTarget}
	claim := func() task.Task {
		t.Helper()
		var id string
		if err := f.pool.QueryRow(ctx, `SELECT id::text FROM tsw_tasks WHERE operation_target_id=$1 AND status='queued'`, secondOperationTarget).Scan(&id); err != nil {
			t.Fatal(err)
		}
		token := uuid.New()
		exec(`UPDATE tsw_tasks SET status='running',lease_owner='partial-test',lease_token=$2,lease_expires_at=now()+interval '10 minutes',attempt_count=1 WHERE id=$1`, id, token)
		return task.Task{ID: id, TaskType: "join", WorkspaceID: f.ids.workspace, OperationTargetID: secondOperationTarget, TargetAccountID: secondTarget, LeaseToken: token, AttemptNo: 1, CorrelationID: "partial-test"}
	}
	login("partial-retry-1")
	item := claim()
	if err := store.FinishJoin(ctx, item, target, "blocked", "oauth_workspace_unavailable", "oauth_workspace_unavailable", "oauth_login", nil); err != nil {
		t.Fatal(err)
	}
	var status, operationStatus string
	if err := f.pool.QueryRow(ctx, `SELECT b.status,o.status FROM tsw_batches b JOIN tsw_operations o ON o.batch_id=b.id WHERE b.id=$1`, batch).Scan(&status, &operationStatus); err != nil {
		t.Fatal(err)
	}
	if status != "serving" || operationStatus != "blocked" {
		t.Fatalf("partial failure lost service or faked success: %s %s", status, operationStatus)
	}
	claimed := publicRedeemRequest(t, f.public, http.MethodPost, "/api/public/v1/redeem/confirm", map[string]any{"cardSecret": secret}, nil)
	if claimed.Code != http.StatusOK {
		t.Fatalf("partial card cannot redeem: %d %s", claimed.Code, claimed.Body.String())
	}
	retry := activateCardIntegrationRequest(t, f.owner, f.ids.membership, f.sessionToken, f.csrfToken, secret, "partial-repeat")
	if retry.Code != http.StatusOK || decodeCardActivation(t, retry).CardId != original.CardId {
		t.Fatal("continuing OAuth changed original card")
	}
	missing := activateCardIntegrationRequest(t, f.owner, uuid.NewString(), f.sessionToken, f.csrfToken, secret, "unfinished-card")
	if missing.Code != http.StatusNotFound {
		t.Fatal("unconfirmed account acquired a card")
	}
	login("partial-retry-2")
	item = claim()
	creds := platform.DeliveryCredentialSet{AccessToken: "fixture-at", RefreshToken: "fixture-rt", IDToken: "fixture-id", PlatformSubjectID: "second-member", WorkspaceID: "workspace-card", ExpiresIn: 3600}
	probe := platform.DeliveryLiveness{Status: "ok", HTTPStatus: 200, PlatformSubjectID: "second-member", WorkspaceID: "workspace-card", ObservedAt: time.Now()}
	member := platform.MembershipResult{Present: true, Complete: true, SeatType: "prolite", PlatformMemberID: "second-member"}
	if err := store.FinishJoinAuthorization(ctx, item, target, member, task.DeliveryTarget{WorkspaceID: f.ids.workspace, PlatformWorkspace: "workspace-card", TargetAccountID: secondTarget}, creds, probe); err != nil {
		t.Fatal(err)
	}
	var membership string
	if err := f.pool.QueryRow(ctx, `SELECT membership_id::text FROM tsw_operation_targets WHERE id=$1`, secondOperationTarget).Scan(&membership); err != nil {
		t.Fatal(err)
	}
	secondSecret := "TSW1-" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x73}, 20))
	second := activateCardIntegrationRequest(t, f.owner, membership, f.sessionToken, f.csrfToken, secondSecret, "partial-second-card")
	if second.Code != http.StatusCreated {
		t.Fatalf("new OAuth success cannot generate its card: %d %s", second.Code, second.Body.String())
	}
	var cards int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM tsw_cards c JOIN tsw_batch_memberships m ON m.id=c.membership_id WHERE m.batch_id=$1`, batch).Scan(&cards); err != nil || cards != 2 {
		t.Fatalf("one card per successful account: %d %v", cards, err)
	}
}
