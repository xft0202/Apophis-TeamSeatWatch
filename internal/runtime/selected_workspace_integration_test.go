//go:build integration

package runtime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/identity"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/migrations"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/workspace"
)

type selectedFixture struct {
	facts platform.SelectedWorkspaceFacts
	err   error
}

func (f *selectedFixture) VerifySelectedWorkspace(_ context.Context, _, _ string) (platform.SelectedWorkspaceFacts, error) {
	return f.facts, f.err
}

func TestSelectedWorkspaceFactsIsolatedAcrossMothersAndWorkspaces(t *testing.T) {
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
	ownerID, motherA, motherB, spaceA, spaceB, childID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	token, tokenHash, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	csrf := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("seed %s: %v", query, err)
		}
	}
	seed(`INSERT INTO tsw_owners(id,username,password_hash) VALUES ($1,'selected-owner','hash')`, ownerID)
	seed(`INSERT INTO tsw_owner_sessions(owner_id,token_hash,auth_version,idle_expires_at,absolute_expires_at) VALUES ($1,$2,1,now()+interval '1 hour',now()+interval '2 hours')`, ownerID, tokenHash[:])
	for i, mother := range []uuid.UUID{motherA, motherB} {
		seed(`INSERT INTO tsw_mother_accounts(id,display_name) VALUES ($1,$2)`, mother, []string{"Mother A", "Mother B"}[i])
		seed(`INSERT INTO tsw_mother_account_credentials(mother_account_id,login_identifier,identifier_hmac,identifier_key_version,password_secret) VALUES ($1,$2,$3,1,'sealed-test')`, mother, []string{"a@example.test", "b@example.test"}[i], bytes.Repeat([]byte{byte(i + 1)}, 32))
		generation, run := uuid.New(), uuid.New()
		seed(`INSERT INTO tsw_mother_personal_sessions(mother_account_id,secret_revision,generation,key_version,nonce,sealed_session,expires_at) VALUES ($1,1,$2,1,$3,$4,now()+interval '1 hour')`, mother, generation, bytes.Repeat([]byte{1}, 12), bytes.Repeat([]byte{2}, 17))
		seed(`INSERT INTO tsw_mother_discoveries(mother_account_id,run_id,secret_revision,session_generation,status) VALUES ($1,$2,1,$3,'discovered')`, mother, run, generation)
	}
	seed(`INSERT INTO tsw_workspaces(id,platform_workspace_id,display_name) VALUES ($1,'canonical-a','A'),($2,'canonical-b','B')`, spaceA, spaceB)
	seed(`INSERT INTO tsw_workspace_projections(workspace_id) VALUES ($1),($2)`, spaceA, spaceB)
	seed(`INSERT INTO tsw_mother_workspace_visibility(mother_account_id,workspace_id,run_id,access_status) SELECT $1,$2,run_id,'readable' FROM tsw_mother_discoveries WHERE mother_account_id=$1`, motherA, spaceA)
	seed(`INSERT INTO tsw_mother_workspace_visibility(mother_account_id,workspace_id,run_id,access_status) SELECT $1,$2,run_id,'readable' FROM tsw_mother_discoveries WHERE mother_account_id=$1`, motherA, spaceB)
	seed(`INSERT INTO tsw_mother_workspace_visibility(mother_account_id,workspace_id,run_id,access_status) SELECT $1,$2,run_id,'readable' FROM tsw_mother_discoveries WHERE mother_account_id=$1`, motherB, spaceA)
	identifier, version, fingerprint, err := identity.Fingerprint(cardIntegrationKeyRing{}, identity.TargetLogin, "child@example.test")
	if err != nil {
		t.Fatal(err)
	}
	seed(`INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES ($1,$2,$3,$4,'Child')`, childID, identifier, fingerprint[:], version)
	origins, _ := auth.ParseOriginPolicy("https://owner.test")
	fixture := &selectedFixture{}
	h := &OwnerAuthHandler{pool: pool, keyRing: cardIntegrationKeyRing{}, origins: origins, selectedWorkspaceReader: fixture}
	serve := ownerapi.Handler(h)
	call := func(method string, workspace, mother uuid.UUID, confirmed bool) (int, ownerapi.SelectedWorkspaceVerification) {
		t.Helper()
		path := "/api/owner/v1/workspaces/" + workspace.String() + "/verification"
		var body []byte
		if method == http.MethodGet {
			path += "?motherAccountId=" + mother.String()
		} else {
			body, _ = json.Marshal(map[string]any{"motherAccountId": mother, "confirmed": confirmed})
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Origin", "https://owner.test")
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		if method == http.MethodPost {
			req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
			req.Header.Set(auth.CSRFHeaderName, csrf)
		}
		rec := httptest.NewRecorder()
		serve.ServeHTTP(rec, req)
		var result ownerapi.SelectedWorkspaceVerification
		if rec.Code == 200 && json.Unmarshal(rec.Body.Bytes(), &result) != nil {
			t.Fatalf("invalid response: %s", rec.Body.String())
		}
		return rec.Code, result
	}
	fact := func() platform.SelectedWorkspaceFacts {
		until := time.Now().Add(30 * 24 * time.Hour)
		seat, member, invite := 5, 1, 1
		return platform.SelectedWorkspaceFacts{Permission: "manage", Result: platform.Result{Outcome: platform.OutcomeOperational, ObservedAt: time.Now().Add(-time.Second), Completeness: platform.Complete, ActiveUntil: &until, SeatLimit: &seat, MemberCount: &member, PendingInviteCount: &invite, Members: []platform.Member{{Kind: "member", Identifier: "child@example.test", PlatformMemberID: "member-1", Status: "active"}, {Kind: "pending_invite", Identifier: "guest@example.test", Status: "pending"}}}}
	}
	seed(`INSERT INTO tsw_workspace_verifications(workspace_id,mother_account_id,source,outcome,permission,completeness,observed_at,expires_at) VALUES($1,$2,'injected_platform_reader','failed','unknown','unknown',now()-interval '8 days',now()-interval '1 day')`, spaceA, motherA)
	if code, stale := call(http.MethodGet, spaceA, motherA, true); code != 200 || stale.Status != "stale" || stale.ActiveUntil != nil {
		t.Fatalf("stale read became current: %d %+v", code, stale)
	}
	if count, err := workspace.NewService(pool, cardIntegrationKeyRing{}).DeleteExpired(ctx, 100); err != nil || count != 1 {
		t.Fatalf("expired verification cleanup count=%d err=%v", count, err)
	}
	if _, pending := call(http.MethodGet, spaceA, motherA, true); pending.Status != "pending" {
		t.Fatalf("cleanup manufactured facts: %+v", pending)
	}
	if code, _ := call(http.MethodGet, spaceB, motherB, true); code != 409 {
		t.Fatalf("unselected mother/space became visible: %d", code)
	}
	for _, invalid := range []struct {
		origin string
		csrf   bool
	}{{"https://foreign.test", true}, {"https://owner.test", false}} {
		body, _ := json.Marshal(map[string]any{"motherAccountId": motherA, "confirmed": true})
		req := httptest.NewRequest(http.MethodPost, "/api/owner/v1/workspaces/"+spaceA.String()+"/verification", bytes.NewReader(body))
		req.Header.Set("Origin", invalid.origin)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		if invalid.csrf {
			req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
			req.Header.Set(auth.CSRFHeaderName, csrf)
		}
		rec := httptest.NewRecorder()
		serve.ServeHTTP(rec, req)
		if rec.Code == 200 {
			t.Fatalf("invalid Origin/CSRF admitted: %+v", invalid)
		}
	}
	fixture.facts = fact()
	for _, pair := range [][2]uuid.UUID{{spaceA, motherA}, {spaceA, motherB}, {spaceB, motherA}} {
		code, result := call(http.MethodPost, pair[0], pair[1], true)
		if code != 200 || result.Status != "verified" || len(result.Members) != 2 || result.Members[0].ChildAccountId == nil || *result.Members[0].ChildAccountId != childID {
			t.Fatalf("verified %v: %d %+v", pair, code, result)
		}
	}
	fixture.facts = fact()
	fixture.facts.Result.Completeness = platform.Partial
	fixture.facts.Result.MemberCount = nil
	code, partial := call(http.MethodPost, spaceA, motherA, true)
	if code != 200 || partial.Status != "partial" || partial.MemberCount != nil || len(partial.Members) != 0 {
		t.Fatalf("partial: %d %+v", code, partial)
	}
	if _, independent := call(http.MethodGet, spaceB, motherA, true); independent.Status != "verified" || independent.MemberCount == nil {
		t.Fatalf("other workspace changed: %+v", independent)
	}
	fixture.err = errors.New("network down")
	_, failed := call(http.MethodPost, spaceA, motherA, true)
	if failed.Status != "failed" || failed.ActiveUntil != nil {
		t.Fatalf("failure retained old facts: %+v", failed)
	}
	if _, other := call(http.MethodGet, spaceA, motherB, true); other.Status != "verified" {
		t.Fatalf("other mother changed: %+v", other)
	}
	fixture.err, fixture.facts = nil, fact()
	fixture.facts.Permission = "read"
	if _, readOnly := call(http.MethodPost, spaceA, motherA, true); readOnly.Status != "partial" || readOnly.ActiveUntil != nil {
		t.Fatalf("read permission became management: %+v", readOnly)
	}
	fixture.facts.Permission = "denied"
	if _, denied := call(http.MethodPost, spaceA, motherA, true); denied.Status != "permission_denied" || len(denied.Members) != 0 {
		t.Fatalf("adapter downgrade leaked facts: %+v", denied)
	}
	seed(`UPDATE tsw_mother_workspace_visibility SET access_status='permission_denied' WHERE mother_account_id=$1 AND workspace_id=$2`, motherB, spaceA)
	if _, denied := call(http.MethodGet, spaceA, motherB, true); denied.Status != "permission_denied" || len(denied.Members) != 0 {
		t.Fatalf("downgrade leaked facts: %+v", denied)
	}
	if code, _ := call(http.MethodPost, spaceA, motherB, true); code != 403 {
		t.Fatalf("downgraded refresh=%d", code)
	}
	// A fresh handler can read durable facts, but removing the adapter fails
	// closed even when an older successful observation remains in storage.
	h = &OwnerAuthHandler{pool: pool, keyRing: cardIntegrationKeyRing{}, origins: origins, selectedWorkspaceReader: &selectedFixture{}}
	serve = ownerapi.Handler(h)
	if _, persisted := call(http.MethodGet, spaceB, motherA, true); persisted.Status != "verified" || len(persisted.Members) != 2 {
		t.Fatalf("restart lost facts: %+v", persisted)
	}
	h = &OwnerAuthHandler{pool: pool, keyRing: cardIntegrationKeyRing{}, origins: origins}
	serve = ownerapi.Handler(h)
	if _, unsupported := call(http.MethodGet, spaceB, motherA, true); unsupported.Status != "pending" || unsupported.ActiveUntil != nil {
		t.Fatalf("missing protocol exposed mock facts: %+v", unsupported)
	}
	if code, _ := call(http.MethodPost, spaceB, motherA, true); code != 501 {
		t.Fatalf("missing protocol=%d", code)
	}
	if code, _ := call(http.MethodPost, spaceB, motherA, false); code != 422 {
		t.Fatalf("unconfirmed selection=%d", code)
	}
}
