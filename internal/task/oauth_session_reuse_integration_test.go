//go:build integration

package task

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/accountsession"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/egress"
	oauthdomain "github.com/xft0202/Apophis-TeamSeatWatch/internal/oauth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

type savedSessionDelivery struct {
	t     *testing.T
	calls int
}

func (a *savedSessionDelivery) CreateDeliveryCredentials(_ context.Context, input platform.DeliveryCredentialRequest) (platform.DeliveryCredentialSet, error) {
	a.calls++
	if input.PersonalSession == nil || input.PersonalSession.AccessToken != "saved-account-at" || input.PersonalSession.Cookies[0].Value != "saved-cookie" {
		a.t.Fatal("step 3 did not receive the saved account session")
	}
	return platform.DeliveryCredentialSet{AccessToken: "workspace-at", RefreshToken: "workspace-rt", IDToken: "id", WorkspaceID: "workspace-platform", PlatformSubjectID: "subject"}, nil
}
func (a *savedSessionDelivery) CheckDeliveryLiveness(context.Context, string, string) (platform.DeliveryLiveness, error) {
	return platform.DeliveryLiveness{Status: oauthdomain.ProbeOK, HTTPStatus: 200, WorkspaceID: "workspace-platform", PlatformSubjectID: "subject", ObservedAt: time.Now()}, nil
}

type noAccountLoginTransport struct{ t *testing.T }

func (r noAccountLoginTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.t.Error("valid saved AT attempted a platform login")
	return nil, errors.New("unexpected platform request")
}

func TestOperationStepThreeUsesAccountManagementSavedSession(t *testing.T) {
	pool, ctx := newRemovalIntegrationPool(t)
	ids := seedOAuthFenceGraph(t, ctx, pool)
	saved := platform.PersonalSession{AccessToken: "saved-account-at", DeviceID: "device", ExpiresAt: time.Now().Add(7 * 24 * time.Hour), Cookies: []platform.SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "saved-cookie"}}}
	version, nonce, ciphertext, err := accountsession.Seal("target", oauthIntegrationRing{}, uuid.MustParse(ids.target), 1, saved)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO tsw_target_personal_access(target_account_id,secret_revision,status) VALUES($1,1,'ready')`, ids.target); err != nil {
		t.Fatal(err)
	}
	generation := uuid.New()
	if _, err = pool.Exec(ctx, `INSERT INTO tsw_target_personal_sessions(target_account_id,secret_revision,attempt,generation,key_version,nonce,sealed_session,expires_at) VALUES($1,1,1,$2,$3,$4,$5,$6)`, ids.target, generation, version, nonce, ciphertext, saved.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	router, err := egress.New(egress.Config{Mode: egress.ModeDirect})
	if err != nil {
		t.Fatal(err)
	}
	defer router.CloseIdleConnections()
	leases, err := egress.NewLeaseManager(router, egress.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := leases.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	lease.Client().Transport = noAccountLoginTransport{t: t}
	var leaseToken uuid.UUID
	if err = pool.QueryRow(ctx, `SELECT lease_token FROM tsw_tasks WHERE id=$1`, ids.oldTask).Scan(&leaseToken); err != nil {
		t.Fatal(err)
	}
	adapter := &savedSessionDelivery{t: t}
	worker := Worker{Store: NewStore(pool, oauthIntegrationRing{}), LeaseTime: time.Minute, DeliveryAdapter: func(*http.Client, platform.Credentials) (platform.DeliveryAdapter, error) { return adapter, nil }}
	item := Task{ID: ids.oldTask, TaskType: "oauth_generate", WorkspaceID: ids.workspace, MembershipID: ids.membership, OAuthAssetID: ids.asset, LeaseToken: leaseToken, AttemptNo: 1, CorrelationID: "session-reuse"}
	if err = worker.runDeliveryGeneration(ctx, item, lease); err != nil {
		t.Fatal(err)
	}
	var unchanged bool
	if err = pool.QueryRow(ctx, `SELECT session.generation=$2 AND session.sealed_session=$3 AND access.attempt=1 FROM tsw_target_personal_sessions session JOIN tsw_target_personal_access access USING(target_account_id) WHERE session.target_account_id=$1`, ids.target, generation, ciphertext).Scan(&unchanged); err != nil || !unchanged || adapter.calls != 1 {
		t.Fatalf("saved account session changed: same=%v calls=%d err=%v", unchanged, adapter.calls, err)
	}
}
