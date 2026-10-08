//go:build integration

package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
	targetdomain "github.com/xft0202/Apophis-TeamSeatWatch/internal/target"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/task"
)

type targetRefreshFixture struct {
	session          platform.PersonalSession
	calls            int
	entered, release chan struct{}
	once             sync.Once
}

func (f *targetRefreshFixture) RefreshPersonal(ctx context.Context, material platform.MotherMaterial) (platform.PersonalRefreshResult, error) {
	if material.LoginIdentifier != "target@example.com" || material.Password != "password" || material.TOTPSecret != "JBSWY3DPEHPK3PXP" {
		return platform.PersonalRefreshResult{}, context.Canceled
	}
	f.calls++
	if f.entered != nil {
		f.once.Do(func() { close(f.entered) })
		select {
		case <-f.release:
		case <-ctx.Done():
			return platform.PersonalRefreshResult{}, ctx.Err()
		}
	}
	return platform.PersonalRefreshResult{Status: "ready", Session: f.session}, nil
}

func TestTargetPersonalExplicitRefreshAndInvalidation(t *testing.T) {
	pool, h, _, sessionToken, csrf := childReviewFixture(t)
	ctx := context.Background()
	if err := sealExistingTargetMaterials(ctx, pool, cardIntegrationKeyRing{}); err != nil {
		t.Fatal(err)
	}
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM tsw_target_accounts WHERE identifier='target@example.com'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	fixture := &targetRefreshFixture{session: platform.PersonalSession{AccessToken: "child-at", DeviceID: "device", Cookies: []platform.SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "secret-cookie"}}, ExpiresAt: time.Now().Add(time.Hour)}}
	h.personalRefresh = fixture
	handler := ownerapi.HandlerWithOptions(h, ownerapi.StdHTTPServerOptions{ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
		if isCSRFBindingError(err) {
			writeProblem(w, r, 403, "csrf_rejected", "Forbidden", "Origin or CSRF validation failed", 0)
			return
		}
		writeProblem(w, r, 400, "invalid_request", "Invalid Request", "Invalid parameters", 0)
	}})
	request := func(method string, target uuid.UUID, authOK, csrfOK bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/owner/v1/target-accounts/"+target.String()+"/personal-session", nil)
		req.Header.Set("Origin", "https://owner.test")
		if authOK {
			req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionToken})
		}
		if csrfOK {
			req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
			req.Header.Set(auth.CSRFHeaderName, csrf)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		for _, cookie := range rec.Result().Cookies() {
			if cookie.Name == auth.SessionCookieName {
				sessionToken = cookie.Value
			}
		}
		return rec
	}
	if r := request("POST", id, false, true); r.Code != 401 {
		t.Fatalf("unauth=%d", r.Code)
	}
	if r := request("POST", id, true, false); r.Code != 403 {
		t.Fatalf("csrf=%d", r.Code)
	}
	if fixture.calls != 0 {
		t.Fatal("unauthorized refresh reached platform")
	}
	if r := request("GET", id, true, false); r.Code != 200 || !strings.Contains(r.Body.String(), "not_verified") {
		t.Fatalf("initial=%d %s", r.Code, r.Body.String())
	}
	rec := request("POST", id, true, true)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "child-at") || strings.Contains(rec.Body.String(), "secret-cookie") {
		t.Fatalf("refresh response=%d %s", rec.Code, rec.Body.String())
	}
	var status ownerapi.TargetPersonalAccess
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil || status.Status != "ready" || status.ExpiresAt == nil || fixture.calls != 1 {
		t.Fatalf("refresh: %+v calls %d err %v", status, fixture.calls, err)
	}
	var ciphertext []byte
	if err := pool.QueryRow(ctx, `SELECT sealed_session FROM tsw_target_personal_sessions WHERE target_account_id=$1`, id).Scan(&ciphertext); err != nil || strings.Contains(string(ciphertext), "child-at") || strings.Contains(string(ciphertext), "secret-cookie") {
		t.Fatal("Personal session not sealed")
	}
	adapterCalls := 0
	probe := SavedTargetPersonalProbe{Pool: pool, KeyRing: cardIntegrationKeyRing{}, Adapter: platform.PersonalUsageProbe{Client: func(context.Context) (*http.Client, func(), error) {
		return &http.Client{Timeout: time.Second, Transport: usageFixtureTransport(func(req *http.Request) (*http.Response, error) {
			adapterCalls++
			if req.Method != "GET" || req.URL.String() != "https://chatgpt.com/backend-api/wham/usage" || req.Header.Get("Authorization") != "Bearer child-at" {
				t.Fatalf("wrong request: %s %s", req.Method, req.URL)
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"rate_limit":{"primary_window":{"used_percent":10}}}`))}, nil
		})}, func() {}, nil
	}}}
	evidence, err := probe.ProbePersonal(ctx, id.String())
	if err != nil || platform.ClassifyPersonalProbe(evidence) != platform.PersonalAvailable || evidence.SessionGeneration == "" || evidence.SessionRevision < 1 || adapterCalls != 1 {
		t.Fatalf("provider %+v %v calls=%d", evidence, err, adapterCalls)
	}
	// Even a previously verified result may not publish after a generation change.
	preview := personalPreview(t, h, sessionToken, csrf, map[string]any{"targetAccountIds": []string{id.String()}})
	batch := decodePersonalBatch(t, personalRequest(t, h, sessionToken, csrf, "POST", "/create", map[string]any{"targetAccountIds": []string{id.String()}, "expectedCount": 1, "confirmed": true, "requestKey": uuid.NewString(), "scopeToken": preview.ScopeToken}), 202)
	stale := personalFixtureProvider{responses: map[string]platform.PersonalProbeEvidence{id.String(): evidence}, onProbe: func(string) {
		if _, err := pool.Exec(ctx, `UPDATE tsw_target_personal_access SET attempt=attempt+1,status='refresh_failed' WHERE target_account_id=$1`, id); err != nil {
			t.Fatal(err)
		}
	}}
	worked, err := task.NewStore(pool, cardIntegrationKeyRing{}).ProcessPersonalProbes(ctx, stale)
	if !worked || err != nil {
		t.Fatalf("stale probe: %t %v", worked, err)
	}
	current := decodePersonalBatch(t, personalRequest(t, h, sessionToken, csrf, "GET", "/"+batch.Id.String(), nil), 200)
	if current.Succeeded != 0 || current.Items[0].Outcome != nil {
		t.Fatalf("stale certified: %+v", current)
	}
	if _, err := pool.Exec(ctx, `UPDATE tsw_target_personal_access access SET attempt=session.attempt,status='ready' FROM tsw_target_personal_sessions session WHERE access.target_account_id=$1 AND session.target_account_id=access.target_account_id`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tsw_target_personal_sessions SET key_version=2 WHERE target_account_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := probe.ProbePersonal(ctx, id.String()); err != task.ErrPersonalCredentialMissing {
		t.Fatalf("invalid envelope reached platform: %v", err)
	}
	if r := request("GET", id, true, false); r.Code != 200 || !strings.Contains(r.Body.String(), "refresh_failed") {
		t.Fatalf("invalid envelope status=%d %s", r.Code, r.Body.String())
	}
	if _, err := pool.Exec(ctx, `UPDATE tsw_target_personal_sessions SET key_version=1,expires_at=now()-interval '1 minute' WHERE target_account_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := probe.ProbePersonal(ctx, id.String()); err != task.ErrPersonalCredentialMissing {
		t.Fatalf("expired session reached platform: %v", err)
	}
	if r := request("GET", id, true, false); r.Code != 200 || !strings.Contains(r.Body.String(), "session_expired") {
		t.Fatalf("expired status=%d %s", r.Code, r.Body.String())
	}
	if adapterCalls != 1 {
		t.Fatalf("invalid or expired credential reached transport: %d", adapterCalls)
	}
	rotated, sealErr := targetdomain.SealMaterial("rotated-password", cardIntegrationKeyRing{})
	if sealErr != nil {
		t.Fatal(sealErr)
	}
	if _, err := pool.Exec(ctx, `UPDATE tsw_target_credentials SET password_secret=$2,secret_revision=secret_revision+1,version=version+1 WHERE target_account_id=$1`, id, rotated); err != nil {
		t.Fatal(err)
	}
	if _, err := probe.ProbePersonal(ctx, id.String()); err != task.ErrPersonalCredentialMissing {
		t.Fatalf("rotated credentials retained session: %v", err)
	}
	if r := request("GET", id, true, false); r.Code != 200 || strings.Contains(r.Body.String(), "ready") {
		t.Fatalf("rotation status=%d %s", r.Code, r.Body.String())
	}
}

type usageFixtureTransport func(*http.Request) (*http.Response, error)

func (f usageFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTargetRefreshStaleAttemptAndIncompleteMaterial(t *testing.T) {
	pool, h, _, sessionToken, csrf := childReviewFixture(t)
	ctx := context.Background()
	if err := sealExistingTargetMaterials(ctx, pool, cardIntegrationKeyRing{}); err != nil {
		t.Fatal(err)
	}
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM tsw_target_accounts WHERE identifier='target@example.com'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	fixture := &targetRefreshFixture{session: platform.PersonalSession{AccessToken: "at", DeviceID: "device", Cookies: []platform.SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "cookie"}}, ExpiresAt: time.Now().Add(time.Hour)}, entered: make(chan struct{}), release: make(chan struct{})}
	h.personalRefresh = fixture
	makeRequest := func() *http.Request {
		req := httptest.NewRequest("POST", "/api/owner/v1/target-accounts/"+id.String()+"/personal-session", nil)
		req.Header.Set("Origin", "https://owner.test")
		req.Header.Set(auth.CSRFHeaderName, csrf)
		req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionToken})
		return req
	}
	results := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.RefreshTargetPersonalAccess(rec, makeRequest(), id, ownerapi.RefreshTargetPersonalAccessParams{})
		results <- rec
	}()
	<-fixture.entered
	if _, err := pool.Exec(ctx, `UPDATE tsw_target_accounts SET status='disabled',version=version+1 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	close(fixture.release)
	rec := <-results
	if rec.Code != 409 {
		t.Fatalf("disabled in-flight refresh=%d %s", rec.Code, rec.Body.String())
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tsw_target_personal_sessions WHERE target_account_id=$1`, id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale session persisted %d %v", count, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tsw_target_accounts SET status='active',version=version+1 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tsw_target_credentials SET material_status='needs_totp',version=version+1 WHERE target_account_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	before := fixture.calls
	rec = httptest.NewRecorder()
	h.RefreshTargetPersonalAccess(rec, makeRequest(), id, ownerapi.RefreshTargetPersonalAccessParams{})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "missing_credentials") || fixture.calls != before {
		t.Fatalf("incomplete material=%d %s calls %d", rec.Code, rec.Body.String(), fixture.calls)
	}
	if _, err := pool.Exec(ctx, `UPDATE tsw_target_credentials SET totp_secret='raw-plaintext',material_status='complete',secret_revision=secret_revision+1,version=version+1 WHERE target_account_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.RefreshTargetPersonalAccess(rec, makeRequest(), id, ownerapi.RefreshTargetPersonalAccessParams{})
	if rec.Code != 500 || fixture.calls != before {
		t.Fatalf("unsealed material reached login: %d calls %d", rec.Code, fixture.calls)
	}
}
