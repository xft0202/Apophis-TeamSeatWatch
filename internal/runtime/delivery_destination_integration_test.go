//go:build integration

package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/migrations"
)

type destinationIntegrationKeyRing struct{ key [32]byte }

func (r destinationIntegrationKeyRing) Current() (uint16, [32]byte) { return 7, r.key }
func (r destinationIntegrationKeyRing) Lookup(version uint16) ([32]byte, bool) {
	return r.key, version == 7
}

type destinationFixtureProbe struct {
	connection ownerapi.DeliveryDestinationTestConnection
	target     ownerapi.DeliveryDestinationTestTarget
	seenSecret string
}

func (p *destinationFixtureProbe) ProbeDestination(_ context.Context, _, _, secret string) (ownerapi.DeliveryDestinationTestConnection, ownerapi.DeliveryDestinationTestTarget) {
	p.seenSecret = secret
	return p.connection, p.target
}

func TestDeliveryDestinationOwnerPersistenceIntegration(t *testing.T) {
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
	ring := destinationIntegrationKeyRing{key: sha256.Sum256([]byte("fixture-deployment-key"))}
	ownerID := uuid.New()
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	csrf := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x53}, 32))
	if _, err = pool.Exec(ctx, `INSERT INTO tsw_owners(id,username,password_hash) VALUES ($1,'owner','hash')`, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO tsw_owner_sessions(owner_id,token_hash,auth_version,idle_expires_at,absolute_expires_at) VALUES ($1,$2,1,now()+interval '1 hour',now()+interval '2 hours')`, ownerID, hash[:]); err != nil {
		t.Fatal(err)
	}
	origins, err := auth.ParseOriginPolicy("https://owner.test")
	if err != nil {
		t.Fatal(err)
	}
	probe := &destinationFixtureProbe{connection: "connected", target: "connected"}
	h := &OwnerAuthHandler{pool: pool, keyRing: ring, origins: origins, destinationProbe: probe}
	server := ownerapi.Handler(h)
	path := "/api/owner/v1/delivery-destinations"
	call := func(method, url string, body any, authorized, csrfEnabled bool) *httptest.ResponseRecorder {
		t.Helper()
		var input []byte
		if body != nil {
			input, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(method, url, bytes.NewReader(input))
		if authorized {
			req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		}
		if csrfEnabled {
			req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
			req.Header.Set(auth.CSRFHeaderName, csrf)
			req.Header.Set("Origin", "https://owner.test")
		} else if method != http.MethodGet {
			req.Header.Set(auth.CSRFHeaderName, csrf)
		}
		resp := httptest.NewRecorder()
		server.ServeHTTP(resp, req)
		return resp
	}
	valid := map[string]any{"name": "Hub", "endpoint": "https://hub.example.test/api/v1", "targetGroup": "42", "secret": "hub-key"}
	if r := call(http.MethodGet, path, nil, false, false); r.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list status=%d", r.Code)
	}
	if r := call(http.MethodPost, path, valid, true, false); r.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d", r.Code)
	}
	if r := call(http.MethodPost, path, valid, false, true); r.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated mutation status=%d", r.Code)
	}
	created := call(http.MethodPost, path, valid, true, true)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var item ownerapi.DeliveryDestination
	if err := json.Unmarshal(created.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if !item.HasSecret || item.Selected || item.Test != nil || strings.Contains(created.Body.String(), "hub-key") {
		t.Fatal("create leaked a secret or certified untested destination")
	}
	var ciphertext []byte
	var nonce []byte
	var version int
	if err := pool.QueryRow(ctx, `SELECT secret_ciphertext,secret_nonce,secret_key_version FROM tsw_delivery_destinations WHERE id=$1`, item.Id).Scan(&ciphertext, &nonce, &version); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("hub-key")) || len(nonce) != 12 || version != 7 {
		t.Fatal("database did not encrypt Hub key")
	}
	if r := call(http.MethodPost, path+"/"+item.Id.String()+"/select", nil, true, true); r.Code != http.StatusConflict {
		t.Fatalf("untested selection=%d", r.Code)
	}
	probe.connection, probe.target = "connected", "target_mismatch"
	mismatch := call(http.MethodPost, path+"/"+item.Id.String()+"/test", nil, true, true)
	if mismatch.Code != http.StatusOK || !strings.Contains(mismatch.Body.String(), "target_mismatch") {
		t.Fatalf("mismatch=%d %s", mismatch.Code, mismatch.Body.String())
	}
	if r := call(http.MethodPost, path+"/"+item.Id.String()+"/select", nil, true, true); r.Code != http.StatusConflict {
		t.Fatalf("mismatch selection=%d", r.Code)
	}
	probe.connection, probe.target = "connected", "connected"
	if r := call(http.MethodPost, path+"/"+item.Id.String()+"/test", nil, true, true); r.Code != http.StatusOK {
		t.Fatalf("test=%d %s", r.Code, r.Body.String())
	}
	if probe.seenSecret != "hub-key" {
		t.Fatal("probe did not receive decrypted Hub key")
	}
	if r := call(http.MethodPost, path+"/"+item.Id.String()+"/select", nil, true, true); r.Code != http.StatusOK {
		t.Fatalf("select=%d %s", r.Code, r.Body.String())
	}
	listed := call(http.MethodGet, path, nil, true, false)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"selected":true`) || strings.Contains(listed.Body.String(), "hub-key") {
		t.Fatalf("redacted list status=%d body=%s", listed.Code, listed.Body.String())
	}
	updated := call(http.MethodPatch, path+"/"+item.Id.String(), map[string]any{"name": "Hub renamed", "endpoint": "https://hub.example.test/api/v1", "targetGroup": "43", "enabled": false}, true, true)
	if updated.Code != http.StatusOK || strings.Contains(updated.Body.String(), "hub-key") {
		t.Fatalf("update=%d %s", updated.Code, updated.Body.String())
	}
	listed = call(http.MethodGet, path, nil, true, false)
	if strings.Contains(listed.Body.String(), `"selected":true`) || strings.Contains(listed.Body.String(), "hub-key") {
		t.Fatal("update did not revoke selection or redact secret")
	}
	if r := call(http.MethodPost, path+"/"+item.Id.String()+"/test", nil, true, true); r.Code != http.StatusConflict {
		t.Fatalf("disabled test=%d", r.Code)
	}
}
