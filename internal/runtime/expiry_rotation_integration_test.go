//go:build integration

package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	targetdomain "github.com/xft0202/Apophis-TeamSeatWatch/internal/target"
)

type mockRotation struct {
	evidence rotationEvidence
	err      error
}

func (m *mockRotation) Evidence(_ context.Context, _, _ uuid.UUID, _ int64) (rotationEvidence, error) {
	return m.evidence, m.err
}
func rotationRequest(h *OwnerAuthHandler, session, csrf, method, path string, body any) *httptest.ResponseRecorder {
	payload, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(payload))
	r.Header.Set("Origin", "https://owner.test")
	r.Header.Set(auth.CSRFHeaderName, csrf)
	r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
	if session != "" {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
	}
	w := httptest.NewRecorder()
	switch {
	case path == "/api/owner/v1/expiry-rotation/previews" && method == "POST":
		h.PreviewExpiryRotation(w, r, ownerapi.PreviewExpiryRotationParams{})
	case path == "/api/owner/v1/expiry-rotation/previews/latest":
		h.GetLatestExpiryRotationPreview(w, r)
	case method == "GET":
		id, _ := uuid.Parse(r.PathValue("previewId"))
		h.GetExpiryRotationPreview(w, r, id)
	case path[len(path)-8:] == "/confirm":
		id, _ := uuid.Parse(path[len("/api/owner/v1/expiry-rotation/previews/") : len(path)-8])
		h.ConfirmExpiryRotation(w, r, id, ownerapi.ConfirmExpiryRotationParams{})
	case path[len(path)-7:] == "/revoke":
		id, _ := uuid.Parse(path[len("/api/owner/v1/expiry-rotation/previews/") : len(path)-7])
		h.RevokeExpiryRotation(w, r, id, ownerapi.RevokeExpiryRotationParams{})
	}
	return w
}
func rotationResult(t *testing.T, w *httptest.ResponseRecorder, status int) ownerapi.ExpiryRotationPreview {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status=%d want=%d: %s", w.Code, status, w.Body.String())
	}
	var p ownerapi.ExpiryRotationPreview
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestExpiryRotationAuthorizationMockOnlyIntegration(t *testing.T) {
	pool, h, owner, session, csrf := childReviewFixture(t)
	ctx := context.Background()
	mother, space, batch, child, original, dest, draft := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	run, generation, exchange := uuid.New(), uuid.New(), uuid.New()
	password, err := targetdomain.SealMaterial("password", h.keyRing)
	if err != nil {
		t.Fatal(err)
	}
	totp, err := targetdomain.SealMaterial("JBSWY3DPEHPK3PXP", h.keyRing)
	if err != nil {
		t.Fatal(err)
	}
	seed := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed %s: %v", sql, err)
		}
	}
	seed(`INSERT INTO tsw_mother_accounts(id,display_name) VALUES($1,'rotation mother')`, mother)
	seed(`INSERT INTO tsw_mother_account_credentials(mother_account_id,login_identifier,identifier_hmac,identifier_key_version,password_secret,totp_secret) VALUES($1,'mother@rotate.test',decode(repeat('31',32),'hex'),1,'pw','totp')`, mother)
	seed(`INSERT INTO tsw_workspaces(id,platform_workspace_id,display_name) VALUES($1,$2,'rotation workspace')`, space, space.String())
	seed(`INSERT INTO tsw_mother_personal_sessions(mother_account_id,secret_revision,generation,key_version,nonce,sealed_session,expires_at) VALUES($1,1,$2,1,decode(repeat('11',12),'hex'),decode(repeat('22',32),'hex'),now()+interval '1 day')`, mother, generation)
	seed(`INSERT INTO tsw_mother_discoveries(mother_account_id,run_id,secret_revision,session_generation,status) VALUES($1,$2,1,$3,'discovered')`, mother, run, generation)
	seed(`INSERT INTO tsw_mother_workspace_visibility(mother_account_id,workspace_id,run_id,access_status) VALUES($1,$2,$3,'readable')`, mother, space, run)
	seed(`INSERT INTO tsw_selected_workspace_tokens(mother_account_id,workspace_id,discovery_run_id,session_generation,secret_revision,exchange_id,status,key_version,nonce,sealed_access,expires_at) VALUES($1,$2,$3,$4,1,$5,'ready',1,decode(repeat('33',12),'hex'),decode(repeat('44',32),'hex'),now()+interval '1 day')`, mother, space, run, generation, exchange)
	var verification int64
	err = pool.QueryRow(ctx, `INSERT INTO tsw_workspace_verifications(workspace_id,mother_account_id,discovery_run_id,session_generation,secret_revision,token_attempt,token_exchange_id,source,outcome,permission,completeness,observed_at,expires_at,active_until,seat_limit,member_count,pending_invite_count) VALUES($1,$2,$3,$4,1,1,$5,'injected_platform_reader','verified','read','complete',now()-interval '1 second',now()+interval '4 minutes',now()-interval '1 second',2,1,1) RETURNING id`, space, mother, run, generation, exchange).Scan(&verification)
	if err != nil {
		t.Fatal(err)
	}
	seed(`INSERT INTO tsw_workspace_verification_entries(verification_id,kind,identifier,identifier_hmac,identifier_key_version,status,platform_member_id) VALUES($1,'member','old@rotate.test',decode(repeat('41',32),'hex'),1,'active','member-1'),($1,'pending_invite','child@rotate.test',decode(repeat('42',32),'hex'),1,'pending',NULL)`, verification)
	seed(`INSERT INTO tsw_standby_child_batches(id,name) VALUES($1,'rotation batch')`, batch)
	seed(`INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,'child@rotate.test',decode(repeat('43',32),'hex'),1,'child')`, child)
	seed(`INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,'old@rotate.test',decode(repeat('45',32),'hex'),1,'original')`, original)
	seed(`INSERT INTO tsw_target_credentials(target_account_id,password_secret,totp_secret,material_status,materials_sealed) VALUES($1,$2,$3,'complete',true)`, child, password, totp)
	seed(`INSERT INTO tsw_standby_child_memberships(target_account_id,batch_id) VALUES($1,$2)`, child, batch)
	seed(`INSERT INTO tsw_delivery_destinations(id,name,endpoint,target_group,secret_key_version,secret_nonce,secret_ciphertext,test_connection,test_target,test_revision,tested_at) VALUES($1,'delivery','https://hub.fixture.test/api/v1','42',1,decode(repeat('11',12),'hex'),decode(repeat('22',32),'hex'),'connected','connected',1,now())`, dest)
	seed(`INSERT INTO tsw_operation_selection_drafts(id,owner_id,version,step,mother_account_id,mother_revision,workspace_id,visibility_run_id,session_generation,verification_id,batch_id,batch_version,children,destination_id,destination_revision) VALUES($1,$2,1,'complete',$3,1,$4,$5,$6,$7,$8,1,$9,$10,1)`, draft, owner, mother, space, run, generation, verification, batch, []byte(`[{"accountId":"`+child.String()+`","membershipVersion":1}]`), dest)
	var tasksBefore, operationsBefore int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tsw_tasks`).Scan(&tasksBefore); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tsw_operations`).Scan(&operationsBefore); err != nil {
		t.Fatal(err)
	}
	assignments := []map[string]any{{"platformMemberId": "member-1", "accountId": child}}
	path := "/api/owner/v1/expiry-rotation/previews"
	preview := func() ownerapi.ExpiryRotationPreview {
		return rotationResult(t, rotationRequest(h, session, csrf, "POST", path, nil), 200)
	}
	without := preview()
	if without.Status != "pending_permission" {
		t.Fatalf("production read-only permission elevated: %+v", without)
	}
	if rotationRequest(h, session, csrf, "POST", path+"/"+without.Id.String()+"/confirm", map[string]any{"confirmed": true, "digest": without.Digest, "idempotencyKey": uuid.New(), "assignments": assignments}).Code != 409 {
		t.Fatal("pending permission authorized")
	}
	if rotationRequest(h, "", csrf, "POST", path, nil).Code != 401 || rotationRequest(h, session, "wrong", "POST", path, nil).Code != 403 {
		t.Fatal("Owner or CSRF missing")
	}
	now := time.Now().UTC()
	proof := func(source, id string) rotationProof {
		return rotationProof{Source: source, EvidenceID: id, ObservedAt: now, ExpiresAt: now.Add(3 * time.Minute)}
	}
	usage := func(id uuid.UUID, state string, ever bool) rotationUsageProof {
		return rotationUsageProof{rotationProof: proof("mock_workspace_usage", "usage-"+id.String()), WorkspaceID: space, AccountID: id, State: state, EverUsed: ever}
	}
	protection := func(id uuid.UUID, status string) rotationProtectionProof {
		return rotationProtectionProof{rotationProof: proof("mock_global_protection", "protection-"+id.String()), AccountID: id, Status: status}
	}
	usedSlot := rotationVerdict{rotationProof: proof("mock_member_protection", "member-1"), AccountID: original, SeatType: "prolite", Usage: usage(original, "used", true), Protection: protection(original, "none"), Decision: "replaceable", Reason: "fixture-used-unprotected"}
	eligibleChild := rotationVerdict{rotationProof: proof("mock_candidate_protection", "candidate-1"), AccountID: child, SeatType: "prolite", Usage: usage(child, "unobserved_prejoin", false), Protection: protection(child, "none"), Decision: "eligible", Reason: "join_candidate_pending_first_probe"}
	eligibleChild.Usage.Absence = &rotationUsageAbsenceProof{rotationProof: proof("mock_usage_ledger_lookup", "complete-absence-1"), WorkspaceID: space, AccountID: child, MotherID: mother, SessionGeneration: generation, VerificationID: verification, Identifier: "child@rotate.test", AccountVersion: 1, CredentialVersion: 1, MembershipVersion: 1, LookupComplete: true, FirstUseRecordStatus: "absent"}
	eligibleChild.Usage.Absence.ExpiresAt = now.Add(2 * time.Minute)
	eligibleChild.Usage.Absence.EvidenceID = rotationAbsenceEvidenceID(*eligibleChild.Usage.Absence)
	mock := &mockRotation{evidence: rotationEvidence{WorkspaceID: space, MotherID: mother, VerificationID: verification, Permission: proof("mock_write_permission", "write-1"), PermissionDecision: "manage", Counts: proof("mock_seat_type_counts", "seats-1"), PaidDefault: proof("mock_paid_default_entitlement", "paid-1"), PaidDefaultEntitlement: 2, SeatTypeCounts: map[string]int{"default": 0, "prolite": 1}, Invitations: map[string]rotationInvitationProof{"child@rotate.test": {rotationProof: proof("mock_invite_seat_type", "invite-1"), WorkspaceID: space, VerificationID: verification, Identifier: "child@rotate.test", Status: "pending", SeatType: "prolite"}}, Slots: map[string]rotationVerdict{"member-1": usedSlot}, Candidates: map[uuid.UUID]rotationVerdict{child: eligibleChild}}}
	h.rotationCapability = mock
	mock.evidence.PermissionDecision = "read"
	if p := preview(); p.Status != "pending_permission" {
		t.Fatalf("permission downgrade accepted: %s", p.Status)
	}
	mock.evidence.PermissionDecision = "manage"
	mock.err = errors.New("protection lookup failed")
	if p := preview(); p.Status != "facts_incomplete" {
		t.Fatalf("protection failure accepted: %s", p.Status)
	}
	mock.err = nil
	delete(mock.evidence.Slots, "member-1")
	if p := preview(); p.Status != "needs_verification" || p.Slots[0].Decision != "needs_verification" {
		t.Fatalf("unproved slot accepted: %+v", p)
	}
	retainedSlot := usedSlot
	retainedSlot.Usage = usage(original, "never_used", false)
	retainedSlot.Decision = "retained"
	mock.evidence.Slots["member-1"] = retainedSlot
	if p := preview(); p.Status != "needs_verification" || p.Slots[0].Decision != "retained" {
		t.Fatalf("retained slot not preserved: %+v", p)
	}
	for _, status := range []string{"delivered", "canceled_retired"} {
		protected := usedSlot
		protected.Protection = protection(original, status)
		protected.Decision = "retained"
		mock.evidence.Slots["member-1"] = protected
		if p := preview(); p.Status != "needs_verification" || p.Slots[0].Decision != "retained" {
			t.Fatalf("%s original slot unprotected: %+v", status, p)
		}
	}
	for _, status := range []string{"sale_reserved", "delivery_pending", "suspected_sold", "unknown"} {
		protected := usedSlot
		protected.Protection = protection(original, status)
		protected.Decision = "needs_verification"
		mock.evidence.Slots["member-1"] = protected
		if p := preview(); p.Status != "needs_verification" || p.Slots[0].Decision != "needs_verification" {
			t.Fatalf("%s original slot authorized: %+v", status, p)
		}
	}
	mock.evidence.Slots["member-1"] = usedSlot
	mock.evidence.SeatTypeCounts = map[string]int{"usage_based": 1}
	if p := preview(); p.Status != "needs_verification" {
		t.Fatalf("mismatched seat type counts accepted: %+v", p)
	}
	mock.evidence.SeatTypeCounts = map[string]int{"prolite": 1, "default": 100}
	if p := preview(); p.Status == "ready" {
		t.Fatalf("typed occupancy greater than complete roster accepted: %+v", p)
	}
	mock.evidence.SeatTypeCounts = map[string]int{"default": 0, "prolite": 1}
	mock.evidence.PaidDefaultEntitlement = 1
	if p := preview(); p.Status != "facts_incomplete" {
		t.Fatalf("paid default entitlement drift accepted: %+v", p)
	}
	mock.evidence.PaidDefaultEntitlement = 2
	protectedChild := eligibleChild
	protectedChild.Protection = protection(child, "delivered")
	protectedChild.Decision = "excluded"
	mock.evidence.Candidates[child] = protectedChild
	if p := preview(); p.Status != "needs_verification" || p.Candidates[0].Decision != "excluded" {
		t.Fatalf("protected candidate accepted: %+v", p)
	}
	for _, status := range []string{"sale_reserved", "delivery_pending", "suspected_sold", "unknown", "canceled_retired"} {
		blocked := eligibleChild
		blocked.Protection = protection(child, status)
		blocked.Decision = "excluded"
		mock.evidence.Candidates[child] = blocked
		if p := preview(); p.Status != "needs_verification" || p.Candidates[0].Decision != "excluded" {
			t.Fatalf("%s protection accepted: %+v", status, p)
		}
	}
	mock.evidence.Candidates[child] = eligibleChild
	unknownUsage := eligibleChild
	unknownUsage.Usage = usage(child, "unknown", false)
	unknownUsage.Decision = "excluded"
	mock.evidence.Candidates[child] = unknownUsage
	if p := preview(); p.Status != "needs_verification" || p.Candidates[0].Decision != "excluded" {
		t.Fatalf("unknown candidate usage accepted: %+v", p)
	}
	for name, corrupt := range map[string]func(*rotationVerdict){
		"missing_lookup":   func(v *rotationVerdict) { v.Usage.Absence = nil },
		"failed_lookup":    func(v *rotationVerdict) { v.Usage.Absence.LookupComplete = false },
		"record_exists":    func(v *rotationVerdict) { v.Usage.Absence.FirstUseRecordStatus = "present" },
		"stale_generation": func(v *rotationVerdict) { v.Usage.Absence.SessionGeneration = uuid.New() },
		"stale_account":    func(v *rotationVerdict) { v.Usage.Absence.AccountVersion++ },
	} {
		t.Run(name, func(t *testing.T) {
			bad := eligibleChild
			bad.Usage.Absence = new(rotationUsageAbsenceProof)
			*bad.Usage.Absence = *eligibleChild.Usage.Absence
			corrupt(&bad)
			mock.evidence.Candidates[child] = bad
			if p := preview(); p.Status == "ready" || p.Candidates[0].Decision == "eligible" {
				t.Fatalf("unproved negative lookup accepted: %+v", p)
			}
		})
	}
	mock.evidence.Candidates[child] = eligibleChild
	delete(mock.evidence.Invitations, "child@rotate.test")
	if p := preview(); p.Status == "ready" || p.Candidates[0].Decision == "eligible" {
		t.Fatalf("invitation without independent seat type proof accepted: %+v", p)
	}
	inviteProof := rotationInvitationProof{rotationProof: proof("mock_invite_seat_type", "invite-1"), WorkspaceID: space, VerificationID: verification, Identifier: "child@rotate.test", Status: "pending", SeatType: "prolite"}
	for name, corrupt := range map[string]func(*rotationInvitationProof){
		"wrong_seat":         func(p *rotationInvitationProof) { p.SeatType = "default" },
		"unknown_seat":       func(p *rotationInvitationProof) { p.SeatType = "unknown" },
		"wrong_identity":     func(p *rotationInvitationProof) { p.Identifier = "other@rotate.test" },
		"wrong_verification": func(p *rotationInvitationProof) { p.VerificationID++ },
		"wrong_source":       func(p *rotationInvitationProof) { p.Source = "official_readonly" },
		"expired":            func(p *rotationInvitationProof) { p.ExpiresAt = now.Add(-time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := inviteProof
			corrupt(&bad)
			mock.evidence.Invitations["child@rotate.test"] = bad
			if p := preview(); p.Status == "ready" || p.Candidates[0].Decision == "eligible" {
				t.Fatalf("unverified invitation accepted: %+v", p)
			}
		})
	}
	mock.evidence.Invitations["child@rotate.test"] = inviteProof
	// The disposable fixture already has a delivery in another Workspace. Moving
	// its membership to the same canonical account tests whole-account protection.
	var deliveredMembership, priorAccount uuid.UUID
	err = pool.QueryRow(ctx, `SELECT membership.id,membership.target_account_id FROM tsw_batch_memberships membership JOIN tsw_target_accounts account ON account.id=membership.target_account_id WHERE account.identifier='target@example.com'`).Scan(&deliveredMembership, &priorAccount)
	if err != nil {
		t.Fatal(err)
	}
	seed(`UPDATE tsw_batch_memberships SET target_account_id=$2 WHERE id=$1`, deliveredMembership, original)
	if p := preview(); p.Status == "ready" || p.Slots[0].Decision != "retained" {
		t.Fatalf("cross-Workspace delivered original was replaceable: %+v", p)
	}
	seed(`UPDATE tsw_batch_memberships SET target_account_id=$2 WHERE id=$1`, deliveredMembership, child)
	if p := preview(); p.Status == "ready" || p.Candidates[0].Decision != "excluded" {
		t.Fatalf("cross-Workspace delivered candidate was eligible: %+v", p)
	}
	seed(`UPDATE tsw_batch_memberships SET target_account_id=$2 WHERE id=$1`, deliveredMembership, priorAccount)
	// A mock pre-join zero claim is still not a measured and persisted post-join
	// scoped zero. No preview-only state can declare this account deliverable.
	prejoinZero := eligibleChild
	prejoinZero.Usage = usage(child, "never_used", false)
	mock.evidence.Candidates[child] = prejoinZero
	if p := preview(); p.Status == "ready" || p.Candidates[0].Decision != "excluded" || p.Candidates[0].DeliveryStatus != "blocked" {
		t.Fatalf("legacy never_used without complete absence lookup accepted: %+v", p)
	}
	prejoinZero.Usage.Absence = eligibleChild.Usage.Absence
	mock.evidence.Candidates[child] = prejoinZero
	if p := preview(); p.Status != "ready" || p.Candidates[0].DeliveryStatus != "join_candidate_pending_first_probe" {
		t.Fatalf("complete absence lookup with legacy state rejected or marked deliverable: %+v", p)
	}
	mock.evidence.Candidates[child] = eligibleChild
	ready := preview()
	if ready.Status != "ready" || ready.PolicyVersion == nil || *ready.PolicyVersion != rotationPreviewPolicyVersion || len(ready.Slots) != 1 || len(ready.Candidates) != 1 || ready.Candidates[0].Decision != "eligible" || ready.Candidates[0].DeliveryStatus != "join_candidate_pending_first_probe" || ready.Candidates[0].UsageState != "unobserved_prejoin" || ready.ExpiresAt.After(eligibleChild.Usage.Absence.ExpiresAt) {
		t.Fatalf("ready evidence rejected: %+v", ready)
	}
	var readyFacts []byte
	if err = pool.QueryRow(ctx, `SELECT facts FROM tsw_expiry_rotation_previews WHERE id=$1`, ready.Id).Scan(&readyFacts); err != nil {
		t.Fatal(err)
	}
	var readyJSON map[string]any
	if err = json.Unmarshal(readyFacts, &readyJSON); err != nil {
		t.Fatal(err)
	}
	if readyJSON["policyVersion"] != float64(1) || readyJSON["digest"] != ready.Digest {
		t.Fatalf("new ready preview did not persist current policy and digest: %s", readyFacts)
	}
	confirmPath := path + "/" + ready.Id.String() + "/confirm"
	key := uuid.New()
	confirmation := map[string]any{"confirmed": true, "digest": ready.Digest, "idempotencyKey": key, "assignments": assignments}
	if w := rotationRequest(h, session, csrf, "POST", path+"/"+ready.Id.String()+"/confirm", map[string]any{"confirmed": true, "digest": ready.Digest, "idempotencyKey": uuid.New(), "assignments": []map[string]any{{"platformMemberId": "wrong", "accountId": child}}}); w.Code != 409 {
		t.Fatalf("incompatible assignment accepted: %d %s", w.Code, w.Body.String())
	}
	mock.evidence.Counts.EvidenceID = "changed-seats"
	if w := rotationRequest(h, session, csrf, "POST", confirmPath, confirmation); w.Code != 409 {
		t.Fatalf("drifting fact accepted: %d %s", w.Code, w.Body.String())
	}
	mock.evidence.Counts.EvidenceID = "seats-1"
	blocked := rotationRequest(h, session, csrf, "POST", confirmPath, confirmation)
	if blocked.Code != 409 || !bytes.Contains(blocked.Body.Bytes(), []byte("pending_write_fence")) {
		t.Fatalf("mock confirmation created authorization: %d %s", blocked.Code, blocked.Body.String())
	}
	resumed := rotationResult(t, rotationRequest(h, session, csrf, "GET", path+"/latest", nil), 200)
	if resumed.Id != ready.Id || resumed.Authorized || resumed.Status != "ready" || resumed.PolicyVersion == nil || *resumed.PolicyVersion != rotationPreviewPolicyVersion {
		t.Fatalf("blocked preview mutated: %+v", resumed)
	}
	// The prior ready format already had join_candidate_pending_first_probe,
	// but did not persist the complete-negative lookup policy version. Its
	// delivery status alone must not make frozen facts join-eligible again.
	for _, oldVersion := range []struct {
		name    string
		version any
	}{
		{name: "missing_policy_version"},
		{name: "old_policy_version", version: float64(0)},
	} {
		t.Run(oldVersion.name, func(t *testing.T) {
			var facts map[string]any
			if err := json.Unmarshal(readyFacts, &facts); err != nil {
				t.Fatal(err)
			}
			facts["id"] = uuid.Nil.String()
			facts["digest"] = ""
			if oldVersion.version == nil {
				delete(facts, "policyVersion")
			} else {
				facts["policyVersion"] = oldVersion.version
			}
			candidateFacts := facts["candidates"].([]any)[0].(map[string]any)
			if facts["status"] != "ready" || candidateFacts["deliveryStatus"] != "join_candidate_pending_first_probe" || candidateFacts["absence"] != nil || candidateFacts["lookupComplete"] != nil {
				t.Fatalf("seed is not old ready JSON with pending-first-probe but no complete-negative lookup: %+v", facts)
			}
			oldDigest := rotationHash(facts)
			facts["digest"] = oldDigest
			oldRaw, err := json.Marshal(facts)
			if err != nil {
				t.Fatal(err)
			}
			var oldID uuid.UUID
			if err = pool.QueryRow(ctx, `INSERT INTO tsw_expiry_rotation_previews(owner_id,draft_id,draft_version,workspace_id,verification_id,facts,digest,status,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,'ready',$8) RETURNING id`, owner, draft, ready.DraftVersion, space, verification, oldRaw, oldDigest, ready.ExpiresAt).Scan(&oldID); err != nil {
				t.Fatal(err)
			}
			var before []byte
			if err = pool.QueryRow(ctx, `SELECT facts FROM tsw_expiry_rotation_previews WHERE id=$1`, oldID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			assertBlocked := func(label string, p ownerapi.ExpiryRotationPreview) {
				t.Helper()
				if p.Id != oldID || p.Status != "needs_verification" || p.Digest != oldDigest || len(p.Candidates) != 1 || p.Candidates[0].Decision != "excluded" || p.Candidates[0].DeliveryStatus != "blocked" || p.Candidates[0].Reason != "legacy_preview_requires_repreview" {
					t.Fatalf("%s resumed pre-policy ready facts: %+v", label, p)
				}
			}
			assertBlocked("latest", rotationResult(t, rotationRequest(h, session, csrf, "GET", path+"/latest", nil), 200))
			get := httptest.NewRequest("GET", path+"/"+oldID.String(), nil)
			get.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
			response := httptest.NewRecorder()
			h.GetExpiryRotationPreview(response, get, oldID)
			assertBlocked("by ID", rotationResult(t, response, 200))
			if w := rotationRequest(h, session, csrf, "POST", path+"/"+oldID.String()+"/confirm", map[string]any{"confirmed": true, "digest": oldDigest, "idempotencyKey": uuid.New(), "assignments": assignments}); w.Code != 409 || !bytes.Contains(w.Body.Bytes(), []byte("needs_verification")) {
				t.Fatalf("old policy confirmation accepted: %d %s", w.Code, w.Body.String())
			}
			var stored []byte
			var digest, status string
			if err = pool.QueryRow(ctx, `SELECT facts,digest,status FROM tsw_expiry_rotation_previews WHERE id=$1`, oldID).Scan(&stored, &digest, &status); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(stored, before) || digest != oldDigest || status != "ready" {
				t.Fatal("GET or confirm changed immutable old-policy facts, digest or status")
			}
		})
	}
	// Pre-upgrade facts have no deliveryStatus. GET/latest may display them
	// only as blocked and stale; neither GET nor confirm rewrites frozen JSON.
	legacy := ready
	legacy.Id = uuid.Nil
	legacy.Candidates = append([]ownerapi.ExpiryRotationCandidate{}, ready.Candidates...)
	legacy.Candidates[0].UsageState = "never_used"
	legacy.Candidates[0].DeliveryStatus = ""
	legacy.Digest = ""
	legacyRaw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var legacyFacts map[string]any
	if err = json.Unmarshal(legacyRaw, &legacyFacts); err != nil {
		t.Fatal(err)
	}
	delete(legacyFacts["candidates"].([]any)[0].(map[string]any), "deliveryStatus")
	legacy.Digest = rotationHash(legacyFacts)
	legacyFacts["digest"] = legacy.Digest
	if legacyRaw, err = json.Marshal(legacyFacts); err != nil {
		t.Fatal(err)
	}
	var legacyID uuid.UUID
	if err = pool.QueryRow(ctx, `INSERT INTO tsw_expiry_rotation_previews(owner_id,draft_id,draft_version,workspace_id,verification_id,facts,digest,status,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,'ready',$8) RETURNING id`, owner, draft, legacy.DraftVersion, space, verification, legacyRaw, legacy.Digest, legacy.ExpiresAt).Scan(&legacyID); err != nil {
		t.Fatal(err)
	}
	var beforeFacts []byte
	var beforeDigest, beforeStatus string
	if err = pool.QueryRow(ctx, `SELECT facts,digest,status FROM tsw_expiry_rotation_previews WHERE id=$1`, legacyID).Scan(&beforeFacts, &beforeDigest, &beforeStatus); err != nil {
		t.Fatal(err)
	}
	legacyLatest := rotationResult(t, rotationRequest(h, session, csrf, "GET", path+"/latest", nil), 200)
	if legacyLatest.Id != legacyID || legacyLatest.Status != "needs_verification" || legacyLatest.Digest != legacy.Digest || legacyLatest.Candidates[0].DeliveryStatus != "blocked" || legacyLatest.Candidates[0].Decision != "excluded" || legacyLatest.Candidates[0].Reason != "legacy_preview_requires_repreview" {
		t.Fatalf("legacy latest silently joinable: %+v", legacyLatest)
	}
	legacyGET := httptest.NewRequest("GET", path+"/"+legacyID.String(), nil)
	legacyGET.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
	legacyResponse := httptest.NewRecorder()
	h.GetExpiryRotationPreview(legacyResponse, legacyGET, legacyID)
	if p := rotationResult(t, legacyResponse, 200); p.Status != "needs_verification" || p.Candidates[0].Decision != "excluded" || p.Digest != legacy.Digest {
		t.Fatalf("legacy GET silently joinable: %+v", p)
	}
	if w := rotationRequest(h, session, csrf, "POST", path+"/"+legacyID.String()+"/confirm", map[string]any{"confirmed": true, "digest": legacy.Digest, "idempotencyKey": uuid.New(), "assignments": assignments}); w.Code != 409 || !bytes.Contains(w.Body.Bytes(), []byte("needs_verification")) {
		t.Fatalf("legacy confirmation accepted: %d %s", w.Code, w.Body.String())
	}
	var afterFacts []byte
	var afterDigest, afterStatus string
	if err = pool.QueryRow(ctx, `SELECT facts,digest,status FROM tsw_expiry_rotation_previews WHERE id=$1`, legacyID).Scan(&afterFacts, &afterDigest, &afterStatus); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeFacts, afterFacts) || beforeDigest != afterDigest || afterDigest != legacy.Digest || beforeStatus != "ready" || afterStatus != "ready" {
		t.Fatal("legacy GET or confirm rewrote frozen facts, digest or status")
	}
	for n := 0; n < 2; n++ {
		if w := rotationRequest(h, session, csrf, "POST", confirmPath, confirmation); w.Code != 409 || !bytes.Contains(w.Body.Bytes(), []byte("pending_write_fence")) {
			t.Fatalf("replay bypassed fence: %d %s", w.Code, w.Body.String())
		}
	}
	if w := rotationRequest(h, session, csrf, "POST", path+"/"+ready.Id.String()+"/revoke", nil); w.Code != 409 {
		t.Fatalf("unauthorized preview revoked: %d", w.Code)
	}
	var authorizationCount, auditCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tsw_expiry_rotation_previews WHERE status='authorized' OR authorized_at IS NOT NULL`).Scan(&authorizationCount); err != nil || authorizationCount != 0 {
		t.Fatalf("authorization persisted: %d %v", authorizationCount, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tsw_audit_events WHERE entity_id=$1 AND event_type IN ('expiry_rotation.authorized','expiry_rotation.revoked')`, ready.Id).Scan(&auditCount); err != nil || auditCount != 0 {
		t.Fatalf("authorization audit incorrectly written: count=%d err=%v", auditCount, err)
	}
	var tasksAfter, operationsAfter int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tsw_tasks`).Scan(&tasksAfter); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tsw_operations`).Scan(&operationsAfter); err != nil {
		t.Fatal(err)
	}
	if tasksBefore != tasksAfter || operationsBefore != operationsAfter {
		t.Fatalf("preview dispatched a mutation: tasks %d/%d operations %d/%d", tasksBefore, tasksAfter, operationsBefore, operationsAfter)
	}
	stale := preview()
	seed(`UPDATE tsw_delivery_destinations SET revision=revision+1,test_revision=NULL,test_connection=NULL,test_target=NULL,tested_at=NULL WHERE id=$1`, dest)
	if w := rotationRequest(h, session, csrf, "POST", path+"/"+stale.Id.String()+"/confirm", map[string]any{"confirmed": true, "digest": stale.Digest, "idempotencyKey": uuid.New(), "assignments": assignments}); w.Code != 409 {
		t.Fatalf("stale destination authorized: %d %s", w.Code, w.Body.String())
	}
	if p := preview(); p.Status != "facts_incomplete" {
		t.Fatalf("changed destination treated as current: %s", p.Status)
	}
	// A newer partial verification supersedes the frozen complete read.
	seed(`INSERT INTO tsw_workspace_verifications(workspace_id,mother_account_id,discovery_run_id,session_generation,secret_revision,token_attempt,token_exchange_id,source,outcome,permission,completeness,observed_at,expires_at) VALUES($1,$2,$3,$4,1,1,$5,'injected_platform_reader','partial','unknown','partial',now(),now()+interval '4 minutes')`, space, mother, run, generation, exchange)
	if p := preview(); p.Status != "facts_incomplete" {
		t.Fatalf("latest partial response accepted: %s", p.Status)
	}
	// A separately complete, future-expiring subscription is not an expired Team.
	var futureVerification int64
	err = pool.QueryRow(ctx, `INSERT INTO tsw_workspace_verifications(workspace_id,mother_account_id,discovery_run_id,session_generation,secret_revision,token_attempt,token_exchange_id,source,outcome,permission,completeness,observed_at,expires_at,active_until,seat_limit,member_count,pending_invite_count) VALUES($1,$2,$3,$4,1,1,$5,'injected_platform_reader','verified','read','complete',now(),now()+interval '4 minutes',now()+interval '1 hour',2,1,1) RETURNING id`, space, mother, run, generation, exchange).Scan(&futureVerification)
	if err != nil {
		t.Fatal(err)
	}
	seed(`INSERT INTO tsw_workspace_verification_entries(verification_id,kind,identifier,identifier_hmac,identifier_key_version,status,platform_member_id) SELECT $2,kind,identifier,identifier_hmac,identifier_key_version,status,platform_member_id FROM tsw_workspace_verification_entries WHERE verification_id=$1`, verification, futureVerification)
	seed(`UPDATE tsw_delivery_destinations SET test_connection='connected',test_target='connected',test_revision=revision,tested_at=now() WHERE id=$1`, dest)
	seed(`UPDATE tsw_operation_selection_drafts SET verification_id=$2,destination_revision=2,version=version+1 WHERE id=$1`, draft, futureVerification)
	mock.evidence.VerificationID = futureVerification
	invite := mock.evidence.Invitations["child@rotate.test"]
	invite.VerificationID = futureVerification
	mock.evidence.Invitations["child@rotate.test"] = invite
	absent := *eligibleChild.Usage.Absence
	absent.VerificationID = futureVerification
	eligibleChild.Usage.Absence = &absent
	mock.evidence.Candidates[child] = eligibleChild
	if p := preview(); p.Candidates[0].Decision != "excluded" || p.Candidates[0].DeliveryStatus != "blocked" {
		t.Fatalf("reused lookup ID on changed verification accepted: %+v", p)
	}
	absent.EvidenceID = rotationAbsenceEvidenceID(absent)
	eligibleChild.Usage.Absence = &absent
	mock.evidence.Candidates[child] = eligibleChild
	if p := preview(); p.Status != "not_expired" || p.Candidates[0].Decision != "eligible" {
		t.Fatalf("future expiry accepted: %s", p.Status)
	}
	// Retained mock history for the exact account in another Workspace is
	// sticky too; a fresh local absence assertion cannot erase everUsed.
	otherSpace := uuid.New()
	seed(`INSERT INTO tsw_workspaces(id,platform_workspace_id,display_name) VALUES($1,$2,'other Workspace')`, otherSpace, otherSpace.String())
	seed(`INSERT INTO tsw_expiry_rotation_previews(owner_id,draft_id,draft_version,workspace_id,verification_id,facts,digest,status,expires_at) VALUES($1,$2,1,$3,$4,$5,$6,'needs_verification',now()+interval '1 minute')`, owner, draft, otherSpace, verification, []byte(`{"source":"mock_capability","candidates":[{"accountId":"`+child.String()+`","everUsed":true}]}`), strings.Repeat("a", 64))
	if p := preview(); p.Candidates[0].Decision != "excluded" || p.Candidates[0].Reason != "sticky_usage_conflict" || p.Candidates[0].DeliveryStatus != "blocked" {
		t.Fatalf("cross-Workspace everUsed downgraded by absence: %+v", p)
	}
	contradictory := eligibleChild
	contradictory.Usage.EverUsed = true
	contradictory.Decision = "excluded"
	mock.evidence.Candidates[child] = contradictory
	if p := preview(); p.Candidates[0].Decision != "excluded" || !p.Candidates[0].EverUsed || p.Candidates[0].DeliveryStatus != "blocked" {
		t.Fatalf("everUsed erased by invalid absence: %+v", p)
	}
	wasUsed := eligibleChild
	wasUsed.Usage = usage(child, "used", true)
	wasUsed.Decision = "excluded"
	mock.evidence.Candidates[child] = wasUsed
	if p := preview(); p.Candidates[0].EverUsed != true || p.Candidates[0].DeliveryStatus != "blocked" {
		t.Fatalf("used observation not persisted: %+v", p)
	}
	mock.evidence.Candidates[child] = eligibleChild
	if p := preview(); p.Candidates[0].Reason != "sticky_usage_conflict" {
		t.Fatalf("sticky usage downgraded: %+v", p)
	}
	// No shared epoch coordinates all writers. Force confirmation to wait on its
	// preview lock while each independent writer commits, then assert that even
	// a previously ready mock preview cannot transition to authorized.
	writers := []struct {
		name, statement string
		args            []any
	}{
		{"new_partial_verification", `INSERT INTO tsw_workspace_verifications(workspace_id,mother_account_id,discovery_run_id,session_generation,secret_revision,token_attempt,token_exchange_id,source,outcome,permission,completeness,observed_at,expires_at) VALUES($1,$2,$3,$4,1,1,$5,'injected_platform_reader','partial','unknown','partial',now(),now()+interval '4 minutes')`, []any{space, mother, run, generation, exchange}},
		{"destination_update", `UPDATE tsw_delivery_destinations SET revision=revision+1,test_revision=NULL,test_connection=NULL,test_target=NULL,tested_at=NULL WHERE id=$1`, []any{dest}},
		{"batch_update", `UPDATE tsw_standby_child_batches SET version=version+1 WHERE id=$1`, []any{batch}},
		{"credential_update", `UPDATE tsw_target_credentials SET material_status='needs_totp',version=version+1 WHERE target_account_id=$1`, []any{child}},
		{"global_delivery_other_workspace", `UPDATE tsw_batch_memberships SET target_account_id=$2 WHERE id=$1`, []any{deliveredMembership, child}},
	}
	for _, writer := range writers {
		t.Run(writer.name, func(t *testing.T) {
			guard, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer guard.Rollback(ctx)
			var locked uuid.UUID
			if err = guard.QueryRow(ctx, `SELECT id FROM tsw_expiry_rotation_previews WHERE id=$1 FOR UPDATE`, ready.Id).Scan(&locked); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- rotationRequest(h, session, csrf, "POST", confirmPath, confirmation) }()
			waitForReviewLock(t, pool, `%FROM tsw_expiry_rotation_previews WHERE id=%FOR UPDATE%`)
			if _, err = pool.Exec(ctx, writer.statement, writer.args...); err != nil {
				t.Fatal(err)
			}
			if err = guard.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case response := <-done:
				if response.Code != 409 || !bytes.Contains(response.Body.Bytes(), []byte("pending_write_fence")) {
					t.Fatalf("stale mock authorized: %d %s", response.Code, response.Body.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("confirmation lock wait deadlocked")
			}
			var count int
			if err = pool.QueryRow(ctx, `SELECT count(*) FROM tsw_expiry_rotation_previews WHERE status='authorized' OR authorized_at IS NOT NULL`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("writer %s created authorization: %d %v", writer.name, count, err)
			}
		})
	}
	seed(`UPDATE tsw_owner_sessions SET revoked_at=now(),revocation_reason='owner_test' WHERE owner_id=$1 AND revoked_at IS NULL`, owner)
	if w := rotationRequest(h, session, csrf, "POST", path, nil); w.Code != 401 {
		t.Fatalf("revoked Owner session previewed: %d", w.Code)
	}
	if w := rotationRequest(h, session, csrf, "POST", confirmPath, confirmation); w.Code != 401 {
		t.Fatalf("revoked Owner session replayed: %d", w.Code)
	}
}
