//go:build integration

package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

func TestTargetPersonalNewerRefreshFencesOlderAttempt(t *testing.T) {
	pool, h, _, sessionToken, csrf := childReviewFixture(t)
	ctx := context.Background()
	if err := sealExistingTargetMaterials(ctx, pool, cardIntegrationKeyRing{}); err != nil {
		t.Fatal(err)
	}
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM tsw_target_accounts WHERE identifier='target@example.com'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	refresher := &sequencedPersonalRefresh{entered: make(chan struct{}), release: make(chan struct{}), session: platform.PersonalSession{AccessToken: "newer-at", DeviceID: "device", Cookies: []platform.SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "cookie"}}, ExpiresAt: time.Now().Add(time.Hour)}}
	h.personalRefresh = refresher
	call := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest("POST", "/api/owner/v1/target-accounts/"+id.String()+"/personal-session", nil)
		request.Header.Set("Origin", "https://owner.test")
		request.Header.Set(auth.CSRFHeaderName, csrf)
		request.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
		request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionToken})
		recorder := httptest.NewRecorder()
		h.RefreshTargetPersonalAccess(recorder, request, id, ownerapi.RefreshTargetPersonalAccessParams{})
		return recorder
	}
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- call() }()
	<-refresher.entered
	// A live attempt is deduplicated; only an abandoned attempt may be superseded.
	if _, err := pool.Exec(ctx, `UPDATE tsw_target_personal_access SET checked_at=now()-interval '2 minutes' WHERE target_account_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	newer := call()
	if newer.Code != 200 {
		t.Fatalf("newer attempt=%d %s", newer.Code, newer.Body.String())
	}
	close(refresher.release)
	older := <-first
	if older.Code != 409 {
		t.Fatalf("stale older attempt=%d %s", older.Code, older.Body.String())
	}
	var accessAttempt, sessionAttempt int64
	if err := pool.QueryRow(ctx, `SELECT access.attempt,session.attempt FROM tsw_target_personal_access access JOIN tsw_target_personal_sessions session USING(target_account_id) WHERE access.target_account_id=$1`, id).Scan(&accessAttempt, &sessionAttempt); err != nil || accessAttempt != 2 || sessionAttempt != 2 {
		t.Fatalf("newer generation not fenced: %d %d %v", accessAttempt, sessionAttempt, err)
	}
}
