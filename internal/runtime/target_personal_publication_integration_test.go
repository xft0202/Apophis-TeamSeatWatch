//go:build integration

package runtime

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
	targetdomain "github.com/xft0202/Apophis-TeamSeatWatch/internal/target"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/task"
)

// A credential edit holds the target lock while the transport finishes. The
// worker must not publish its old generation before that edit commits, nor
// report the old usage result once the edit has revoked the generation.
func TestTargetPersonalPublicationWaitsForCredentialRotation(t *testing.T) {
	pool, h, _, ownerSession, csrf := childReviewFixture(t)
	ctx := context.Background()
	if err := sealExistingTargetMaterials(ctx, pool, cardIntegrationKeyRing{}); err != nil {
		t.Fatal(err)
	}
	var id uuid.UUID
	var revision int64
	if err := pool.QueryRow(ctx, `SELECT target.id,credential.secret_revision FROM tsw_target_accounts target JOIN tsw_target_credentials credential ON credential.target_account_id=target.id WHERE target.identifier='target@example.com'`).Scan(&id, &revision); err != nil {
		t.Fatal(err)
	}
	session := platform.PersonalSession{AccessToken: "old-at", DeviceID: "device", Cookies: []platform.SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "cookie"}}, ExpiresAt: time.Now().Add(time.Hour)}
	version, nonce, sealed, err := sealSessionFor("target", cardIntegrationKeyRing{}, id, revision, session)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO tsw_target_personal_access(target_account_id,secret_revision,status) VALUES($1,$2,'ready')`, id, revision); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO tsw_target_personal_sessions(target_account_id,secret_revision,attempt,generation,key_version,nonce,sealed_session,expires_at) VALUES($1,$2,1,$3,$4,$5,$6,$7)`, id, revision, uuid.New(), version, nonce, sealed, session.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	preview := personalPreview(t, h, ownerSession, csrf, map[string]any{"targetAccountIds": []string{id.String()}})
	batch := decodePersonalBatch(t, personalRequest(t, h, ownerSession, csrf, "POST", "/create", map[string]any{"targetAccountIds": []string{id.String()}, "expectedCount": 1, "confirmed": true, "requestKey": uuid.NewString(), "scopeToken": preview.ScopeToken}), 202)
	entered, release := make(chan struct{}), make(chan struct{})
	probe := SavedTargetPersonalProbe{Pool: pool, KeyRing: cardIntegrationKeyRing{}, Adapter: platform.PersonalUsageProbe{Client: func(context.Context) (*http.Client, func(), error) {
		return &http.Client{Timeout: 5 * time.Second, Transport: usageFixtureTransport(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("Authorization") != "Bearer old-at" {
				t.Fatal("wrong saved token")
			}
			close(entered)
			<-release
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"rate_limit":{"allowed":true}}`))}, nil
		})}, func() {}, nil
	}}}
	finished := make(chan error, 1)
	go func() {
		_, probeErr := task.NewStore(pool, cardIntegrationKeyRing{}).ProcessPersonalProbes(ctx, probe)
		finished <- probeErr
	}()
	<-entered
	rotation, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rotation.Rollback(ctx)
	password, err := targetdomain.SealMaterial("new-password", cardIntegrationKeyRing{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rotation.Exec(ctx, `UPDATE tsw_target_credentials SET password_secret=$2,secret_revision=secret_revision+1,version=version+1 WHERE target_account_id=$1`, id, password); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err = <-finished:
		t.Fatalf("old result published before revocation commit: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if err = rotation.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("publication did not release after rotation")
	}
	current := decodePersonalBatch(t, personalRequest(t, h, ownerSession, csrf, "GET", "/"+batch.Id.String(), nil), 200)
	if current.Succeeded != 0 || current.Items[0].Outcome != nil || current.Items[0].VerifiedEvidence {
		t.Fatalf("old generation certified after revocation: %+v", current)
	}
}
