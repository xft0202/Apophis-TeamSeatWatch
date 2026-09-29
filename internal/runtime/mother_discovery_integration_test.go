//go:build integration

package runtime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/migrations"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

type fixtureDiscovery struct {
	result platform.DiscoveryResult
	calls  int
}

func (f *fixtureDiscovery) Discover(_ context.Context, session platform.PersonalSession) (platform.DiscoveryResult, error) {
	if session.AccessToken != "personal-token" {
		panic("discovery did not receive saved Personal session")
	}
	f.calls++
	return f.result, nil
}

type fixturePersonalRefresh struct {
	result platform.PersonalRefreshResult
	calls  int
}

func (f *fixturePersonalRefresh) RefreshPersonal(_ context.Context, material platform.MotherMaterial) (platform.PersonalRefreshResult, error) {
	if material.Password != "password" || material.TOTPSecret == "" {
		panic("refresh did not receive saved mother material")
	}
	f.calls++
	return f.result, nil
}

func TestMotherDiscoveryPersistsPerMotherVisibilityWithoutBindingOrImplicitSelection(t *testing.T) {
	dsn := os.Getenv("TSW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TSW_TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err = migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ownerID, firstID, secondID, missingID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	session, sessionHash, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	csrf := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x77}, 32))
	for _, query := range []struct {
		text string
		args []any
	}{
		{`INSERT INTO tsw_owners(id,username,password_hash) VALUES ($1,'owner','hash')`, []any{ownerID}},
		{`INSERT INTO tsw_owner_sessions(owner_id,token_hash,auth_version,idle_expires_at,absolute_expires_at) VALUES ($1,$2,1,now()+interval '1 hour',now()+interval '2 hours')`, []any{ownerID, sessionHash[:]}},
		{`INSERT INTO tsw_mother_accounts(id,display_name) VALUES ($1,'Mother A'),($2,'Mother B'),($3,'Missing')`, []any{firstID, secondID, missingID}},
	} {
		if _, err := pool.Exec(ctx, query.text, query.args...); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range []uuid.UUID{firstID, secondID, missingID} {
		secret := "JBSWY3DPEHPK3PXP"
		if i == 2 {
			secret = ""
		}
		_, err := pool.Exec(ctx, `INSERT INTO tsw_mother_account_credentials(mother_account_id,login_identifier,identifier_hmac,identifier_key_version,password_secret,totp_secret) VALUES ($1,$2,$3,1,'password',NULLIF($4,'')::bytea)`, id, []string{"a@example.test", "b@example.test", "missing@example.test"}[i], bytes.Repeat([]byte{byte(i + 1)}, 32), secret)
		if err != nil {
			t.Fatal(err)
		}
	}
	origins, _ := auth.ParseOriginPolicy("https://owner.test")
	refresh := &fixturePersonalRefresh{result: platform.PersonalRefreshResult{Status: "ready", Session: platform.PersonalSession{AccessToken: "personal-token", DeviceID: "device-one", Cookies: []platform.SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "cookie-secret"}}, ExpiresAt: time.Now().Add(time.Hour)}}}
	adapter := &fixtureDiscovery{result: platform.DiscoveryResult{Status: "discovered", Workspaces: []platform.DiscoveredWorkspace{{PlatformID: "team-one", Name: "One", Access: "readable"}, {PlatformID: "team-two", Name: "Two", Access: "permission_denied"}}}}
	handler := &OwnerAuthHandler{pool: pool, keyRing: cardIntegrationKeyRing{}, origins: origins, personalRefresh: refresh, discovery: adapter}
	serve := ownerapi.HandlerWithOptions(handler, ownerapi.StdHTTPServerOptions{ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
		if isCSRFBindingError(err) {
			writeProblem(w, r, 403, "csrf_rejected", "Forbidden", "Origin or CSRF validation failed", 0)
			return
		}
		writeProblem(w, r, 400, "invalid_request", "Invalid Request", "Invalid parameters", 0)
	}})
	call := func(method, path string, authorize, csrfHeader bool) (int, []byte) {
		t.Helper()
		req := httptest.NewRequest(method, "/api/owner/v1/mother-accounts/"+path, nil)
		req.Header.Set("Origin", "https://owner.test")
		if authorize {
			req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
		}
		if csrfHeader {
			req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
			req.Header.Set(auth.CSRFHeaderName, csrf)
		}
		rec := httptest.NewRecorder()
		serve.ServeHTTP(rec, req)
		for _, cookie := range rec.Result().Cookies() {
			if cookie.Name == auth.SessionCookieName {
				session = cookie.Value
			}
		}
		return rec.Code, rec.Body.Bytes()
	}
	readDiscovery := func(method string, id uuid.UUID, authorize, csrfHeader bool) (int, ownerapi.MotherDiscovery) {
		status, body := call(method, id.String()+"/discovery", authorize, csrfHeader)
		var result ownerapi.MotherDiscovery
		if status == 200 && json.Unmarshal(body, &result) != nil {
			t.Fatalf("invalid discovery: %s", body)
		}
		return status, result
	}
	readAccess := func(method string, id uuid.UUID, authorize, csrfHeader bool) (int, ownerapi.MotherPersonalAccess) {
		status, body := call(method, id.String()+"/personal-session", authorize, csrfHeader)
		var result ownerapi.MotherPersonalAccess
		if status == 200 && json.Unmarshal(body, &result) != nil {
			t.Fatalf("invalid access: %s", body)
		}
		return status, result
	}
	listRequest := httptest.NewRequest(http.MethodGet, "/api/owner/v1/mother-accounts?page=1&page_size=100", nil)
	listRequest.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
	listResponse := httptest.NewRecorder()
	serve.ServeHTTP(listResponse, listRequest)
	var list ownerapi.MotherAccountList
	if listResponse.Code != 200 || json.Unmarshal(listResponse.Body.Bytes(), &list) != nil || list.Total != 3 {
		t.Fatalf("saved mother list: %d %s", listResponse.Code, listResponse.Body.String())
	}
	if status, _ := readAccess(http.MethodPost, firstID, false, true); status != 401 {
		t.Fatalf("unauthenticated refresh=%d", status)
	}
	if status, _ := readAccess(http.MethodPost, firstID, true, false); status != 403 {
		t.Fatalf("missing CSRF refresh=%d", status)
	}
	if status, _ := readDiscovery(http.MethodPost, firstID, true, false); status != 403 {
		t.Fatalf("missing CSRF discovery=%d", status)
	}
	if refresh.calls != 0 || adapter.calls != 0 {
		t.Fatal("adapter called without authorization")
	}
	if status, result := readDiscovery(http.MethodGet, firstID, true, false); status != 200 || result.Status != "not_verified" {
		t.Fatalf("materials triggered discovery: %d %+v", status, result)
	}
	if status, result := readDiscovery(http.MethodPost, firstID, true, true); status != 200 || result.Status != "missing_credentials" || adapter.calls != 0 {
		t.Fatalf("discovered without Personal session: %d %+v", status, result)
	}
	if status, result := readAccess(http.MethodPost, missingID, true, true); status != 200 || result.Status != "missing_credentials" || refresh.calls != 0 {
		t.Fatalf("missing material refresh: %d %+v", status, result)
	}
	for _, id := range []uuid.UUID{firstID, secondID} {
		if status, result := readAccess(http.MethodPost, id, true, true); status != 200 || result.Status != "ready" {
			t.Fatalf("Personal refresh: %d %+v", status, result)
		}
	}
	var encrypted []byte
	if err := pool.QueryRow(ctx, `SELECT sealed_session FROM tsw_mother_personal_sessions WHERE mother_account_id=$1`, firstID).Scan(&encrypted); err != nil || bytes.Contains(encrypted, []byte("personal-token")) || bytes.Contains(encrypted, []byte("cookie-secret")) {
		t.Fatalf("personal session not encrypted: %v", err)
	}
	if status, result := readDiscovery(http.MethodPost, firstID, true, true); status != 200 || result.Status != "discovered" || len(result.Workspaces) != 2 {
		t.Fatalf("first discovery: %d %+v", status, result)
	}
	adapter.result.Workspaces = []platform.DiscoveredWorkspace{{PlatformID: "team-one", Name: "Renamed One", Access: "unknown"}}
	if status, result := readDiscovery(http.MethodPost, secondID, true, true); status != 200 || result.Status != "discovered" || len(result.Workspaces) != 1 {
		t.Fatalf("second discovery: %d %+v", status, result)
	}
	_, one := readDiscovery(http.MethodGet, firstID, true, false)
	_, two := readDiscovery(http.MethodGet, secondID, true, false)
	if one.Workspaces[0].Id != two.Workspaces[0].Id || one.Workspaces[0].AccessStatus != "readable" || two.Workspaces[0].AccessStatus != "unknown" {
		t.Fatalf("canonical identity/per-mother access: %+v %+v", one, two)
	}
	var spaces, relations, bindings int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM tsw_workspaces),(SELECT count(*) FROM tsw_mother_workspace_visibility),(SELECT count(*) FROM tsw_mother_workspace_bindings)`).Scan(&spaces, &relations, &bindings); err != nil || spaces != 2 || relations != 3 || bindings != 0 {
		t.Fatalf("spaces=%d relations=%d bindings=%d err=%v", spaces, relations, bindings, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tsw_mother_personal_sessions SET expires_at=now()-interval '1 second' WHERE mother_account_id=$1`, firstID); err != nil {
		t.Fatal(err)
	}
	if status, result := readDiscovery(http.MethodGet, firstID, true, false); status != 200 || result.Status != "not_verified" || len(result.Workspaces) != 0 {
		t.Fatalf("expired Personal session exposed old visibility: %d %+v", status, result)
	}
	if _, err := pool.Exec(ctx, `UPDATE tsw_mother_personal_sessions SET expires_at=now()+interval '30 minutes' WHERE mother_account_id=$1`, firstID); err != nil {
		t.Fatal(err)
	}
	adapter.result = platform.DiscoveryResult{Status: "session_expired"}
	if status, result := readDiscovery(http.MethodPost, firstID, true, true); status != 200 || result.Status != "session_expired" || len(result.Workspaces) != 0 {
		t.Fatalf("expired session: %d %+v", status, result)
	}
	if status, result := readAccess(http.MethodGet, firstID, true, false); status != 200 || result.Status != "not_verified" {
		t.Fatalf("revoked session still ready: %d %+v", status, result)
	}
	adapter.result = platform.DiscoveryResult{Status: "discovered"}
	if status, result := readDiscovery(http.MethodPost, secondID, true, true); status != 200 || result.Status != "empty" {
		t.Fatalf("empty: %d %+v", status, result)
	}
	adapter.result = platform.DiscoveryResult{Status: "permission_denied"}
	if status, result := readDiscovery(http.MethodPost, secondID, true, true); status != 200 || result.Status != "permission_denied" {
		t.Fatalf("permission: %d %+v", status, result)
	}
	adapter.result = platform.DiscoveryResult{Status: "discovery_failed"}
	if status, result := readDiscovery(http.MethodPost, secondID, true, true); status != 200 || result.Status != "discovery_failed" {
		t.Fatalf("failure: %d %+v", status, result)
	}
	if adapter.calls != 6 {
		t.Fatalf("unexpected discovery calls=%d", adapter.calls)
	}
	refresh.result = platform.PersonalRefreshResult{Status: "invalid_login"}
	if status, result := readAccess(http.MethodPost, firstID, true, true); status != 200 || result.Status != "invalid_login" {
		t.Fatalf("invalid login refresh: %d %+v", status, result)
	}
	if status, result := readDiscovery(http.MethodGet, firstID, true, false); status != 200 || result.Status != "not_verified" {
		t.Fatalf("failed refresh did not invalidate discovery: %d %+v", status, result)
	}
	refresh.result = platform.PersonalRefreshResult{Status: "refresh_failed"}
	if status, result := readAccess(http.MethodPost, firstID, true, true); status != 200 || result.Status != "refresh_failed" {
		t.Fatalf("refresh transport failure: %d %+v", status, result)
	}
	handler.personalRefresh = nil
	if status, result := readAccess(http.MethodPost, firstID, true, true); status != 200 || result.Status != "unavailable" {
		t.Fatalf("production refresh must fail closed: %d %+v", status, result)
	}
	handler.discovery = nil
	if status, result := readDiscovery(http.MethodPost, secondID, true, true); status != 200 || result.Status != "unavailable" {
		t.Fatalf("production default: %d %+v", status, result)
	}
	_, err = pool.Exec(ctx, `UPDATE tsw_mother_account_credentials SET password_secret='new-password',secret_revision=secret_revision+1,version=version+1 WHERE mother_account_id=$1`, secondID)
	if err != nil {
		t.Fatal(err)
	}
	if status, result := readAccess(http.MethodGet, secondID, true, false); status != 200 || result.Status != "not_verified" {
		t.Fatalf("revision must invalidate Personal session: %d %+v", status, result)
	}
	if status, result := readDiscovery(http.MethodGet, secondID, true, false); status != 200 || result.Status != "not_verified" {
		t.Fatalf("revision must invalidate discovery: %d %+v", status, result)
	}
}
