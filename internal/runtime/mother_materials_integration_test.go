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
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/migrations"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/mothersecret"
)

func TestMotherMaterialsStartupMigrationImportEditExportAndTamper(t *testing.T) {
	dsn := os.Getenv("TSW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TSW_TEST_DATABASE_URL required")
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
	ownerID, legacyID := uuid.New(), uuid.New()
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tsw_owners(id,username,password_hash) VALUES ($1,'owner','hash')`, []any{ownerID}},
		{`INSERT INTO tsw_owner_sessions(owner_id,token_hash,auth_version,idle_expires_at,absolute_expires_at) VALUES ($1,$2,1,now()+interval '1 hour',now()+interval '2 hours')`, []any{ownerID, hash[:]}},
		{`INSERT INTO tsw_mother_accounts(id,display_name) VALUES ($1,'Legacy')`, []any{legacyID}},
		{`INSERT INTO tsw_mother_account_credentials(mother_account_id,login_identifier,identifier_hmac,identifier_key_version,password_secret,totp_secret) VALUES ($1,'legacy@example.test',decode(repeat('25',32),'hex'),1,'legacy-password','JBSWY3DPEHPK3PXP')`, []any{legacyID}},
	} {
		if _, err := pool.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	origins, _ := auth.ParseOriginPolicy("https://owner.test")
	server, closeServer, err := NewOwnerAuthHandler(OwnerAuthConfig{DatabaseURL: dsn, KeyRing: cardIntegrationKeyRing{}, Origins: origins})
	if err != nil {
		t.Fatalf("startup migration: %v", err)
	}
	defer closeServer()
	csrf := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x33}, 32))
	request := func(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Origin", "https://owner.test")
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		if method != http.MethodGet {
			req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
			req.Header.Set(auth.CSRFHeaderName, csrf)
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		for _, cookie := range rec.Result().Cookies() {
			if cookie.Name == auth.SessionCookieName {
				token = cookie.Value
			}
		}
		return rec
	}
	var oldPassword []byte
	var revision int64
	if err := pool.QueryRow(ctx, `SELECT password_secret,secret_revision FROM tsw_mother_account_credentials WHERE mother_account_id=$1`, legacyID).Scan(&oldPassword, &revision); err != nil || !mothersecret.IsSealed(oldPassword) || revision != 2 {
		t.Fatalf("startup left legacy plaintext: %v revision=%d", err, revision)
	}
	created := request(http.MethodPost, "/api/owner/v1/mother-accounts", `{"displayName":"New","loginIdentifier":"new@example.test","password":"new-password","totpSecret":"JBSWY3DPEHPK3PXP"}`, nil)
	var account ownerapi.MotherAccount
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &account) != nil {
		t.Fatalf("create %d %s", created.Code, created.Body.String())
	}
	imported := request(http.MethodPost, "/api/owner/v1/mother-accounts/import", `{"content":"import@example.test----import-password----JBSWY3DPEHPK3PXP"}`, nil)
	if imported.Code != 200 || !strings.Contains(imported.Body.String(), `"imported":1`) {
		t.Fatalf("import %d %s", imported.Code, imported.Body.String())
	}
	export := func(expected int) *httptest.ResponseRecorder {
		return request(http.MethodPost, "/api/owner/v1/mother-accounts/export", `{"expectedCount":`+strconv.Itoa(expected)+`,"confirmed":true}`, nil)
	}
	output := export(3)
	if output.Code != 200 || !strings.Contains(output.Body.String(), "legacy@example.test----legacy-password----JBSWY3DPEHPK3PXP") || !strings.Contains(output.Body.String(), "new@example.test----new-password----JBSWY3DPEHPK3PXP") || !strings.Contains(output.Body.String(), "import@example.test----import-password----JBSWY3DPEHPK3PXP") {
		t.Fatalf("export status=%d or content mismatch", output.Code)
	}
	patch := request(http.MethodPatch, "/api/owner/v1/mother-accounts/"+account.Id.String(), `{"displayName":"New","status":"active","totpSecret":"KRSXG5DSNFXGOIDB"}`, map[string]string{"If-Match": `"1"`})
	if patch.Code != 200 {
		t.Fatalf("patch TOTP: %d %s", patch.Code, patch.Body.String())
	}
	output = export(3)
	if output.Code != 200 || !strings.Contains(output.Body.String(), "new@example.test----new-password----KRSXG5DSNFXGOIDB") {
		t.Fatalf("updated export status=%d or content mismatch", output.Code)
	}
	patch = request(http.MethodPatch, "/api/owner/v1/mother-accounts/"+account.Id.String(), `{"displayName":"New","status":"active","password":"next-password"}`, map[string]string{"If-Match": `"2"`})
	if patch.Code != 200 {
		t.Fatalf("patch password status=%d", patch.Code)
	}
	output = export(3)
	if output.Code != 200 || !strings.Contains(output.Body.String(), "new@example.test----next-password----KRSXG5DSNFXGOIDB") {
		t.Fatalf("password edit failed status=%d", output.Code)
	}
	var sealedPassword, sealedTOTP []byte
	if err := pool.QueryRow(ctx, `SELECT password_secret,totp_secret FROM tsw_mother_account_credentials WHERE mother_account_id=$1`, account.Id).Scan(&sealedPassword, &sealedTOTP); err != nil || !mothersecret.IsSealed(sealedPassword) || !mothersecret.IsSealed(sealedTOTP) || bytes.Contains(sealedPassword, []byte("next-password")) {
		t.Fatalf("new materials at rest: %v", err)
	}
	sealedPassword[len(sealedPassword)-1] ^= 1
	if _, err := pool.Exec(ctx, `UPDATE tsw_mother_account_credentials SET password_secret=$2,secret_revision=secret_revision+1,version=version+1 WHERE mother_account_id=$1`, account.Id, sealedPassword); err != nil {
		t.Fatal(err)
	}
	output = export(3)
	if output.Code != 500 || strings.Contains(output.Body.String(), "next-password") || strings.Contains(output.Body.String(), "KRSXG5DS") {
		t.Fatalf("tampered export status=%d or secret exposed", output.Code)
	}
	patch = request(http.MethodPatch, "/api/owner/v1/mother-accounts/"+account.Id.String(), `{"displayName":"New","status":"active","password":"overwrite-corrupt"}`, map[string]string{"If-Match": `"3"`})
	if patch.Code != 500 {
		t.Fatalf("tampered edit status=%d", patch.Code)
	}
}
