//go:build integration

package runtime

import (
	"bytes"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"net/http"
	"net/http/httptest"
	"testing"
)

func removalRequest(h *OwnerAuthHandler, session, csrf, method string, preview, slot uuid.UUID, action string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, "/api/owner/v1/expiry-rotation/previews/"+preview.String()+"/removal", bytes.NewReader(raw))
	r.Header.Set("Origin", "https://owner.test")
	r.Header.Set(auth.CSRFHeaderName, csrf)
	r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
	w := httptest.NewRecorder()
	switch action {
	case "start":
		h.StartRotationRemoval(w, r, preview, ownerapi.StartRotationRemovalParams{})
	case "get":
		h.GetRotationRemoval(w, r, preview)
	case "run":
		h.RunRotationRemovalSlot(w, r, preview, slot, ownerapi.RunRotationRemovalSlotParams{})
	case "verify":
		h.VerifyRotationRemovalSlot(w, r, preview, slot, ownerapi.VerifyRotationRemovalSlotParams{})
	case "stop":
		h.StopRotationRemoval(w, r, preview, ownerapi.StopRotationRemovalParams{})
	}
	return w
}

func TestSlotRemovalCommitsOnlyVerifiedAbsenceAndSameKeyDoesNotDeleteAgain(t *testing.T) {
	f := newRemovalFixture(t, 1)
	pending := f.start(t)
	if len(pending.Slots) != 1 || pending.Slots[0].State != "pending" || pending.Slots[0].CandidateReady {
		t.Fatalf("unexpected initial slot: %+v", pending)
	}
	done := f.action(t, pending.Slots[0].Id, "run")
	if done.Slots[0].State != "absent_verified" || !done.Slots[0].CandidateReady || done.Slots[0].UncertainObligation || done.Slots[0].VerificationId == nil {
		t.Fatalf("absence not committed: %+v", done)
	}
	replay := f.start(t)
	f.action(t, replay.Slots[0].Id, "run")
	if f.http.calls != 1 {
		t.Fatalf("same-key replay deleted %d times", f.http.calls)
	}
}

func TestSlotRemovalReverificationRefreshesEvidenceWithoutDeletingAgain(t *testing.T) {
	f := newRemovalFixture(t, 1)
	p := f.start(t)
	first := f.action(t, p.Slots[0].Id, "run")
	second := f.action(t, p.Slots[0].Id, "verify")
	if !second.Slots[0].CandidateReady || second.Slots[0].VerificationId == nil || *first.Slots[0].VerificationId == *second.Slots[0].VerificationId || second.Slots[0].LeaseEpoch != 2 || f.http.calls != 1 {
		t.Fatalf("fresh evidence was not renewable: %+v", second)
	}
}

func TestSlotRemovalHistoryRestoresCommittedWorkWithoutRemoteReads(t *testing.T) {
	f := newRemovalFixture(t, 1)
	p := f.start(t)
	before := f.http.reads
	r := httptest.NewRequest("GET", "/api/owner/v1/expiry-rotation/removals", nil)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: f.session})
	w := httptest.NewRecorder()
	f.h.ListRotationRemovals(w, r, ownerapi.ListRotationRemovalsParams{})
	if w.Code != 200 {
		t.Fatalf("history failed: %d %s", w.Code, w.Body.String())
	}
	var history ownerapi.RotationRemovalHistory
	if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if history.Total != 1 || len(history.Items) != 1 || history.Items[0].PreviewId != p.PreviewId || history.Items[0].WorkspaceName != "removal fixture" || f.http.reads != before || f.http.calls != 0 {
		t.Fatalf("history widened or queried remote: %+v", history)
	}
}

func TestSlotRemovalRejectsMissingTicket10Authorization(t *testing.T) {
	_, h, _, session, csrf := childReviewFixture(t)
	result := removalRequest(h, session, csrf, "POST", uuid.New(), uuid.Nil, "start", map[string]any{"confirmed": true, "authorizationDigest": string(bytes.Repeat([]byte("a"), 64)), "idempotencyKey": uuid.New()})
	if result.Code != 404 {
		t.Fatalf("unknown authorization created removal: %d %s", result.Code, result.Body.String())
	}
}
