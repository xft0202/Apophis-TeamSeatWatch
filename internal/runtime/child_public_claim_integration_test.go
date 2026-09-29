//go:build integration

package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	oauthdomain "github.com/xft0202/Apophis-TeamSeatWatch/internal/oauth"
)

func TestPublicFirstClaimWaitsForMaterialRepairButOriginalRestoreAndDownloadSurvive(t *testing.T) {
	pool, _, _, _, _ := childReviewFixture(t)
	ctx := context.Background()
	var membershipID string
	if err := pool.QueryRow(ctx, `SELECT id FROM tsw_batch_memberships LIMIT 1`).Scan(&membershipID); err != nil {
		t.Fatal(err)
	}
	secret := "TSW1-" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x55}, 20))
	keyVersion, lookup, err := oauthdomain.LookupHMAC(cardIntegrationKeyRing{}, secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO tsw_cards(id,membership_id,hmac_key_version,lookup_hmac,display_suffix,redemption_deadline) VALUES($1,$2,$3,$4,'55AAAAAA',now()+interval '1 day')`, uuid.NewString(), membershipID, keyVersion, lookup[:]); err != nil {
		t.Fatal(err)
	}
	handler := NewPublicRedeemHandler(pool, cardIntegrationKeyRing{}, nil)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE tsw_target_credentials SET material_status='needs_totp',version=version+1 WHERE target_account_id=(SELECT target_account_id FROM tsw_batch_memberships WHERE id=$1)`, membershipID); err != nil {
		t.Fatal(err)
	}
	var started sync.WaitGroup
	started.Add(1)
	finished := make(chan *httpResponse, 1)
	go func() {
		started.Done()
		response := publicRedeemRequest(t, handler, http.MethodPost, "/api/public/v1/redeem/confirm", map[string]any{"cardSecret": secret}, nil)
		finished <- &httpResponse{code: response.Code, body: response.Body.String()}
	}()
	started.Wait()
	select {
	case response := <-finished:
		t.Fatalf("claim raced pending material update: %d %s", response.code, response.body)
	case <-time.After(100 * time.Millisecond):
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-finished:
		if response.code != 404 {
			t.Fatalf("claim with missing 2FA=%d %s", response.code, response.body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("claim did not release after material update")
	}
	if _, err = pool.Exec(ctx, `UPDATE tsw_target_credentials SET material_status='complete',version=version+1 WHERE target_account_id=(SELECT target_account_id FROM tsw_batch_memberships WHERE id=$1)`, membershipID); err != nil {
		t.Fatal(err)
	}
	claimed := publicRedeemRequest(t, handler, http.MethodPost, "/api/public/v1/redeem/confirm", map[string]any{"cardSecret": secret}, nil)
	if claimed.Code != 200 {
		t.Fatalf("claim after repair=%d %s", claimed.Code, claimed.Body.String())
	}
	cookie := claimed.Result().Cookies()[0]
	if _, err = pool.Exec(ctx, `UPDATE tsw_target_credentials SET material_status='needs_totp',version=version+1 WHERE target_account_id=(SELECT target_account_id FROM tsw_batch_memberships WHERE id=$1)`, membershipID); err != nil {
		t.Fatal(err)
	}
	restored := publicRedeemRequest(t, handler, http.MethodPost, "/api/public/v1/redeem/confirm", map[string]any{"cardSecret": secret}, nil)
	if restored.Code != 200 {
		t.Fatalf("existing customer's immutable order restore=%d %s", restored.Code, restored.Body.String())
	}
	for _, accessCookie := range []*http.Cookie{cookie, restored.Result().Cookies()[0]} {
		downloaded := publicRedeemRequest(t, handler, http.MethodPost, "/api/public/v1/redeem/download", nil, accessCookie)
		if downloaded.Code != 200 || downloaded.Body.String() != "{}" {
			t.Fatalf("immutable download after 2FA changed=%d %s", downloaded.Code, downloaded.Body.String())
		}
	}
}

func TestPublicClaimAndIdempotentCardActivationUseTheSameLockOrder(t *testing.T) {
	pool, owner, _, session, csrf := childReviewFixture(t)
	ctx := context.Background()
	var membershipID string
	if err := pool.QueryRow(ctx, `SELECT id FROM tsw_batch_memberships LIMIT 1`).Scan(&membershipID); err != nil {
		t.Fatal(err)
	}
	secret := "TSW1-" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x56}, 20))
	keyVersion, lookup, err := oauthdomain.LookupHMAC(cardIntegrationKeyRing{}, secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO tsw_cards(id,membership_id,hmac_key_version,lookup_hmac,display_suffix,redemption_deadline) VALUES($1,$2,$3,$4,'56AAAAAA',now()+interval '1 day')`, uuid.NewString(), membershipID, keyVersion, lookup[:]); err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	var cardID string
	if err = blocker.QueryRow(ctx, `SELECT id FROM tsw_cards WHERE membership_id=$1 FOR UPDATE`, membershipID).Scan(&cardID); err != nil {
		t.Fatal(err)
	}
	activationDone := make(chan *httpResponse, 1)
	go func() {
		response := activateCardIntegrationRequest(t, owner, membershipID, session, csrf, secret, "same-card")
		activationDone <- &httpResponse{code: response.Code, body: response.Body.String()}
	}()
	waitForPublicLock(t, pool, "SELECT id,membership_id,status,display_suffix,redemption_deadline,hmac_key_version,lookup_hmac%")

	claimDone := make(chan *httpResponse, 1)
	public := NewPublicRedeemHandler(pool, cardIntegrationKeyRing{}, nil)
	go func() {
		response := publicRedeemRequest(t, public, http.MethodPost, "/api/public/v1/redeem/confirm", map[string]any{"cardSecret": secret}, nil)
		claimDone <- &httpResponse{code: response.Code, body: response.Body.String()}
	}()
	// Claim must wait for the credential held by activation, not acquire the
	// card lock first and deadlock after activation gets the card.
	waitForPublicLock(t, pool, "SELECT credential.material_status FROM tsw_batch_memberships membership%")
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, result := range []<-chan *httpResponse{activationDone, claimDone} {
		select {
		case response := <-result:
			if response.code != http.StatusOK {
				t.Fatalf("concurrent activation/claim=%d %s", response.code, response.body)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent activation/claim did not finish")
		}
	}
}

func waitForPublicLock(t *testing.T, pool *pgxpool.Pool, queryPattern string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		var waiting bool
		if err := pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND query LIKE $1 AND wait_event_type='Lock')`, queryPattern).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
	}
	t.Fatalf("query never waited for the expected lock: %s", queryPattern)
}

type httpResponse struct {
	code int
	body string
}
