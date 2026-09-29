//go:build integration

package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	targetdomain "github.com/xft0202/Apophis-TeamSeatWatch/internal/target"
)

func operationDraftRequest(t *testing.T, h *OwnerAuthHandler, session, csrf, method string, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(body)
	r := httptest.NewRequest(method, "/api/owner/v1/operation-draft", bytes.NewReader(payload))
	r.Header.Set("Origin", "https://owner.test")
	r.Header.Set(auth.CSRFHeaderName, csrf)
	r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
	if session != "" {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
	}
	w := httptest.NewRecorder()
	switch method {
	case "GET":
		h.GetOperationDraft(w, r)
	case "POST":
		h.StartOperationDraft(w, r, ownerapi.StartOperationDraftParams{})
	case "PATCH":
		h.ChangeOperationDraft(w, r, ownerapi.ChangeOperationDraftParams{})
	}
	return w
}
func operationDraftBatchPage(t *testing.T, h *OwnerAuthHandler, session, csrf string, batch uuid.UUID, page int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/owner/v1/operation-draft/batches/"+batch.String()+"/children", nil)
	r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
	if session != "" {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
	}
	w := httptest.NewRecorder()
	p := ownerapi.Page(page)
	h.ListOperationDraftBatchChildren(w, r, batch, ownerapi.ListOperationDraftBatchChildrenParams{Page: &p})
	return w
}
func draftResult(t *testing.T, response *httptest.ResponseRecorder, code int) ownerapi.OperationDraft {
	t.Helper()
	if response.Code != code {
		t.Fatalf("draft status %d want %d: %s", response.Code, code, response.Body.String())
	}
	var d ownerapi.OperationDraft
	if err := json.Unmarshal(response.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	return d
}
func TestOperationDraftFrozenSelectionPersistenceAndConcurrencyIntegration(t *testing.T) {
	pool, h, _, session, csrf := childReviewFixture(t)
	ctx := context.Background()
	mother, space, batch, child, other, dest := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	run, generation, exchange := uuid.New(), uuid.New(), uuid.New()
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
		{`INSERT INTO tsw_selected_workspace_tokens(mother_account_id,workspace_id,discovery_run_id,session_generation,secret_revision,exchange_id,status,key_version,nonce,sealed_access,expires_at) VALUES($1,$2,$3,$4,1,$5,'ready',1,decode(repeat('33',12),'hex'),decode(repeat('44',32),'hex'),now()+interval '1 day')`, []any{mother, space, run, generation, exchange}},
		{`INSERT INTO tsw_workspace_verifications(workspace_id,mother_account_id,discovery_run_id,session_generation,secret_revision,token_attempt,token_exchange_id,source,outcome,permission,completeness,observed_at,expires_at,active_until,seat_limit,member_count,pending_invite_count) VALUES($1,$2,$3,$4,1,1,$5,'injected_platform_reader','verified','read','complete',now(),now()+interval '1 day',now()+interval '30 days',20,0,0)`, []any{space, mother, run, generation, exchange}},
		{`INSERT INTO tsw_standby_child_batches(id,name) VALUES($1,'first')`, []any{batch}},
		{`INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,$2,$3,1,$2)`, []any{child, "one@fixture.test", bytes.Repeat([]byte{0x47}, 32)}},
		{`INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,$2,$3,1,$2)`, []any{other, "two@fixture.test", bytes.Repeat([]byte{0x48}, 32)}},
		{`INSERT INTO tsw_target_credentials(target_account_id,password_secret,totp_secret,material_status,materials_sealed) VALUES($1,$2,$3,'complete',true)`, []any{child, sealedPassword, sealedTotp}},
		{`INSERT INTO tsw_target_credentials(target_account_id,password_secret,material_status,materials_sealed) VALUES($1,$2,'needs_totp',true)`, []any{other, sealedPassword}},
		{`INSERT INTO tsw_standby_child_memberships(target_account_id,batch_id) VALUES($1,$2)`, []any{child, batch}},
		{`INSERT INTO tsw_delivery_destinations(id,name,endpoint,target_group,secret_key_version,secret_nonce,secret_ciphertext,test_connection,test_target,test_revision,tested_at) VALUES($1,'hub','https://hub.fixture.test/api/v1','42',1,decode(repeat('11',12),'hex'),decode(repeat('22',32),'hex'),'connected','connected',1,now())`, []any{dest}},
	}
	for _, s := range statements {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("fixture: %v\n%s", err, s.sql)
		}
	}
	page := operationDraftBatchPage(t, h, session, csrf, batch, 1)
	if page.Code != 200 {
		t.Fatalf("child labels status=%d: %s", page.Code, page.Body.String())
	}
	var listed ownerapi.OperationDraftBatchChildren
	if err := json.Unmarshal(page.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Total != 1 || len(listed.Items) != 1 || listed.Items[0].Identifier != "one@fixture.test" || listed.Items[0].MembershipVersion != 1 || listed.Items[0].MaterialStatus != "complete" {
		t.Fatalf("batch labels %+v", listed)
	}
	if operationDraftBatchPage(t, h, "", csrf, batch, 1).Code != 401 {
		t.Fatal("anonymous child labels allowed")
	}
	get := func() ownerapi.OperationDraft {
		return draftResult(t, operationDraftRequest(t, h, session, csrf, "GET", nil), 200)
	}
	start := draftResult(t, operationDraftRequest(t, h, session, csrf, "POST", nil), 200)
	if start.Step != "mother" || start.Version != 1 {
		t.Fatalf("empty draft: %+v", start)
	}
	change := func(version int64, choice string, fields map[string]any) ownerapi.OperationDraft {
		body := map[string]any{"expectedVersion": version, "choice": choice}
		for k, v := range fields {
			body[k] = v
		}
		return draftResult(t, operationDraftRequest(t, h, session, csrf, "PATCH", body), 200)
	}
	d := change(start.Version, "mother", map[string]any{"motherAccountId": mother})
	d = change(d.Version, "workspace", map[string]any{"workspaceId": space})
	if d.VerificationId == nil || d.WorkspaceId == nil || *d.WorkspaceId != space || !d.WorkspaceCurrent {
		t.Fatalf("workspace not captured: %+v", d)
	}
	d = change(d.Version, "children", map[string]any{"batchId": batch, "batchVersion": 1, "children": []map[string]any{{"accountId": child, "membershipVersion": 1}}})
	d = change(d.Version, "destination", map[string]any{"destinationId": dest})
	if d.Step != "complete" || !d.DestinationCurrent || len(d.Children) != 1 {
		t.Fatalf("not complete: %+v", d)
	}
	// Restarting handler (same database) and refreshing must resume, never create a new draft.
	another := &OwnerAuthHandler{pool: pool, origins: h.origins}
	resumed := draftResult(t, operationDraftRequest(t, another, session, csrf, "POST", nil), 200)
	if resumed.Id != start.Id || resumed.Version != d.Version || get().Children[0].AccountId != child {
		t.Fatalf("resume changed selection: %+v", resumed)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO tsw_standby_child_memberships(target_account_id,batch_id) VALUES($1,$2)`, other, batch); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tsw_standby_child_batches SET version=version+1 WHERE id=$1`, batch); err != nil {
		t.Fatal(err)
	}
	frozen := get()
	if frozen.BatchCurrent || len(frozen.Children) != 1 || frozen.Children[0].AccountId != child || frozen.DestinationCurrent {
		t.Fatalf("batch update silently expanded scope: %+v", frozen)
	}
	// A changed destination revision never rewrites the frozen destination ID or revision.
	if _, err := pool.Exec(ctx, `UPDATE tsw_delivery_destinations SET revision=revision+1,test_revision=NULL,test_connection=NULL,test_target=NULL,tested_at=NULL WHERE id=$1`, dest); err != nil {
		t.Fatal(err)
	}
	if get().DestinationRevision == nil || *get().DestinationRevision != 1 {
		t.Fatal("destination snapshot rewritten")
	}
	// Competing tabs updating one revision: only one succeeds.
	d = change(d.Version, "back", map[string]any{"backTo": "mother"})
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- operationDraftRequest(t, h, session, csrf, "PATCH", map[string]any{"expectedVersion": d.Version, "choice": "mother", "motherAccountId": mother}).Code
		}()
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for code := range results {
		if code == 200 {
			success++
		} else if code == 409 {
			conflict++
		} else {
			t.Fatalf("competing tab status %d", code)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("CAS success=%d conflict=%d", success, conflict)
	}
	reset := get()
	if reset.WorkspaceId != nil || reset.BatchId != nil || reset.DestinationId != nil || len(reset.Children) != 0 {
		t.Fatalf("mother change retained downstream: %+v", reset)
	}
	reset = change(reset.Version, "workspace", map[string]any{"workspaceId": space})
	if operationDraftRequest(t, h, session, csrf, "PATCH", map[string]any{"expectedVersion": reset.Version, "choice": "children", "batchId": batch, "batchVersion": 1, "children": []map[string]any{{"accountId": child, "membershipVersion": 1}}}).Code != 409 {
		t.Fatal("stale batch revision accepted")
	}
	if _, err := pool.Exec(ctx, `UPDATE tsw_standby_child_memberships SET version=version+1 WHERE target_account_id=$1`, child); err != nil {
		t.Fatal(err)
	}
	if operationDraftRequest(t, h, session, csrf, "PATCH", map[string]any{"expectedVersion": reset.Version, "choice": "children", "batchId": batch, "batchVersion": 2, "children": []map[string]any{{"accountId": child, "membershipVersion": 1}}}).Code != 409 {
		t.Fatal("stale child membership accepted")
	}
	reset = change(reset.Version, "children", map[string]any{"batchId": batch, "batchVersion": 2, "children": []map[string]any{{"accountId": child, "membershipVersion": 2}}})
	if operationDraftRequest(t, h, session, csrf, "PATCH", map[string]any{"expectedVersion": reset.Version, "choice": "destination", "destinationId": dest}).Code != 409 {
		t.Fatal("untested destination revision accepted")
	}
	if _, err := pool.Exec(ctx, `UPDATE tsw_delivery_destinations SET test_connection='connected',test_target='connected',test_revision=revision,tested_at=now() WHERE id=$1`, dest); err != nil {
		t.Fatal(err)
	}
	reset = change(reset.Version, "destination", map[string]any{"destinationId": dest})
	reset = change(reset.Version, "back", map[string]any{"backTo": "workspace"})
	reset = change(reset.Version, "workspace", map[string]any{"workspaceId": space})
	if reset.BatchId != nil || reset.DestinationId != nil || len(reset.Children) != 0 || reset.Step != "children" {
		t.Fatalf("workspace re-selection retained downstream data: %+v", reset)
	}
	incomplete := operationDraftRequest(t, h, session, csrf, "PATCH", map[string]any{"expectedVersion": reset.Version, "choice": "children", "batchId": batch, "batchVersion": 2, "children": []map[string]any{{"accountId": other, "membershipVersion": 1}}})
	if incomplete.Code != 409 || !bytes.Contains(incomplete.Body.Bytes(), []byte("child_material_incomplete")) {
		t.Fatalf("missing child material not repairable: %d %s", incomplete.Code, incomplete.Body.String())
	}
	if _, err := pool.Exec(ctx, `UPDATE tsw_target_credentials SET totp_secret=$2,material_status='complete',secret_revision=secret_revision+1,version=version+1 WHERE target_account_id=$1`, other, sealedTotp); err != nil {
		t.Fatal(err)
	}
	reset = change(reset.Version, "children", map[string]any{"batchId": batch, "batchVersion": 2, "children": []map[string]any{{"accountId": other, "membershipVersion": 1}}})
	if reset.Children[0].AccountId != other || reset.BatchId == nil {
		t.Fatal("child repair did not resume exact prior step")
	}
	if operationDraftRequest(t, h, "", csrf, "GET", nil).Code != 401 {
		t.Fatal("anonymous read allowed")
	}
	if operationDraftRequest(t, h, session, "wrong", "PATCH", map[string]any{"expectedVersion": reset.Version, "choice": "mother", "motherAccountId": mother}).Code != 403 {
		t.Fatal("CSRF mutation allowed")
	}
}
