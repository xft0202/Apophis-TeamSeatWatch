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
	err    error
	calls  int
}

func (f *fixtureDiscovery) VerifyAndDiscover(_ context.Context, _ platform.MotherMaterial) (platform.DiscoveryResult, error) {
	f.calls++
	return f.result, f.err
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
	adapter := &fixtureDiscovery{result: platform.DiscoveryResult{Status: "discovered", Workspaces: []platform.DiscoveredWorkspace{{PlatformID: "team-one", Name: "One", Access: "readable"}, {PlatformID: "team-two", Name: "Two", Access: "permission_denied"}}}}
	handler := &OwnerAuthHandler{pool: pool, origins: origins, discovery: adapter}
	serve := ownerapi.HandlerWithOptions(handler, ownerapi.StdHTTPServerOptions{ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
		if isCSRFBindingError(err) {
			writeProblem(w, r, 403, "csrf_rejected", "Forbidden", "Origin or CSRF validation failed", 0)
			return
		}
		writeProblem(w, r, 400, "invalid_request", "Invalid Request", "Invalid parameters", 0)
	}})
	call := func(method string, id uuid.UUID, authorize, csrfHeader bool) (int, ownerapi.MotherDiscovery) {
		t.Helper()
		req := httptest.NewRequest(method, "/api/owner/v1/mother-accounts/"+id.String()+"/discovery", nil)
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
		var result ownerapi.MotherDiscovery
		if rec.Code == 200 && json.Unmarshal(rec.Body.Bytes(), &result) != nil {
			t.Fatalf("invalid response: %s", rec.Body.String())
		}
		return rec.Code, result
	}
	listRequest := httptest.NewRequest(http.MethodGet, "/api/owner/v1/mother-accounts?page=1&page_size=100", nil)
	listRequest.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
	listResponse := httptest.NewRecorder()
	serve.ServeHTTP(listResponse, listRequest)
	var list ownerapi.MotherAccountList
	if listResponse.Code != 200 || json.Unmarshal(listResponse.Body.Bytes(), &list) != nil || list.Total != 3 {
		t.Fatalf("saved mother selection list: %d %s", listResponse.Code, listResponse.Body.String())
	}
	if status, _ := call(http.MethodPost, firstID, false, true); status != 401 {
		t.Fatalf("unauthenticated POST=%d", status)
	}
	if status, _ := call(http.MethodPost, firstID, true, false); status != 403 {
		t.Fatalf("missing CSRF=%d", status)
	}
	if adapter.calls != 0 {
		t.Fatal("remote adapter called without auth/CSRF")
	}
	if status, result := call(http.MethodGet, firstID, true, false); status != 200 || result.Status != "not_verified" || len(result.Workspaces) != 0 {
		t.Fatalf("saving credentials triggered discovery: %d %+v", status, result)
	}
	if status, result := call(http.MethodPost, firstID, true, true); status != 200 || result.Status != "discovered" || len(result.Workspaces) != 2 || result.Workspaces[1].AccessStatus != "permission_denied" {
		t.Fatalf("first result: %d %+v", status, result)
	}
	adapter.result.Workspaces = []platform.DiscoveredWorkspace{{PlatformID: "team-one", Name: "Renamed One", Access: "unknown"}}
	if status, result := call(http.MethodPost, secondID, true, true); status != 200 || result.Status != "discovered" || len(result.Workspaces) != 1 {
		t.Fatalf("second result: %d %+v", status, result)
	}
	_, one := call(http.MethodGet, firstID, true, false)
	_, two := call(http.MethodGet, secondID, true, false)
	if one.Workspaces[0].Id != two.Workspaces[0].Id || one.Workspaces[0].AccessStatus != "readable" || two.Workspaces[0].AccessStatus != "unknown" {
		t.Fatalf("canonical identity/per-mother access lost: %+v %+v", one, two)
	}
	var spaces, relations, bindings int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM tsw_workspaces),(SELECT count(*) FROM tsw_mother_workspace_visibility),(SELECT count(*) FROM tsw_mother_workspace_bindings)`).Scan(&spaces, &relations, &bindings); err != nil || spaces != 2 || relations != 3 || bindings != 0 {
		t.Fatalf("spaces=%d relations=%d bindings=%d err=%v", spaces, relations, bindings, err)
	}
	adapter.result = platform.DiscoveryResult{Status: "invalid_login"}
	if status, result := call(http.MethodPost, firstID, true, true); status != 200 || result.Status != "invalid_login" || len(result.Workspaces) != 0 {
		t.Fatalf("invalid login: %d %+v", status, result)
	}
	adapter.result = platform.DiscoveryResult{Status: "discovered"}
	if status, result := call(http.MethodPost, secondID, true, true); status != 200 || result.Status != "empty" {
		t.Fatalf("empty result: %d %+v", status, result)
	}
	adapter.result = platform.DiscoveryResult{Status: "permission_denied"}
	if status, result := call(http.MethodPost, secondID, true, true); status != 200 || result.Status != "permission_denied" {
		t.Fatalf("permission: %d %+v", status, result)
	}
	adapter.result = platform.DiscoveryResult{Status: "discovery_failed"}
	if status, result := call(http.MethodPost, secondID, true, true); status != 200 || result.Status != "discovery_failed" {
		t.Fatalf("failure: %d %+v", status, result)
	}
	if status, result := call(http.MethodPost, missingID, true, true); status != 200 || result.Status != "missing_credentials" {
		t.Fatalf("missing material: %d %+v", status, result)
	}
	if adapter.calls != 6 {
		t.Fatalf("unexpected adapter calls=%d", adapter.calls)
	}
	handler.discovery = nil
	if status, result := call(http.MethodPost, firstID, true, true); status != 200 || result.Status != "unavailable" || len(result.Workspaces) != 0 {
		t.Fatalf("production default: %d %+v", status, result)
	}
	_, err = pool.Exec(ctx, `UPDATE tsw_mother_account_credentials SET password_secret='new-password',secret_revision=secret_revision+1,version=version+1 WHERE mother_account_id=$1`, firstID)
	if err != nil {
		t.Fatal(err)
	}
	if status, result := call(http.MethodGet, firstID, true, false); status != 200 || result.Status != "not_verified" {
		t.Fatalf("revision must invalidate discovery: %d %+v", status, result)
	}
	if _, err := pool.Exec(ctx, `UPDATE tsw_mother_discoveries SET observed_at=now()-interval '8 days' WHERE mother_account_id=$1`, secondID); err != nil {
		t.Fatal(err)
	}
	if status, result := call(http.MethodGet, secondID, true, false); status != 200 || result.Status != "not_verified" || len(result.Workspaces) != 0 {
		t.Fatalf("stale observation must not enable selection: %d %+v", status, result)
	}
}
