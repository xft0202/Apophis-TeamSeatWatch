//go:build integration

package runtime

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/internalapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/mothersecret"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
	targetdomain "github.com/xft0202/Apophis-TeamSeatWatch/internal/target"
)

// This fixture exposes the compiled product and the registered boundaries. All
// external adapters are in-process mocks; none has a production HTTP transport.
// One original member and one invited candidate exercise the complete workflow.
type frontendFacts struct{ f *removalFixture }

func (frontendFacts) Source() string { return "injected_platform_reader" }
func (m frontendFacts) VerifySelectedWorkspace(_ context.Context, _ platform.WorkspaceAccess, _, _ string) (platform.SelectedWorkspaceFacts, error) {
	m.f.http.mu.Lock()
	members := append([]platform.Member(nil), m.f.http.members...)
	m.f.http.mu.Unlock()
	count, invites, paid := len(members), 1, 2
	members = append(members, platform.Member{Kind: "pending_invite", Identifier: m.f.preview.Candidates[0].Identifier, Status: "pending", SeatType: "prolite"})
	expired := m.f.preview.ActiveUntil
	return platform.SelectedWorkspaceFacts{Permission: "read", Result: platform.Result{ObservedAt: time.Now().UTC(), Outcome: platform.OutcomeOperational, Completeness: platform.Complete, ActiveUntil: &expired, SeatLimit: &paid, MemberCount: &count, PendingInviteCount: &invites, SeatTypeCounts: map[string]int{"default": 0, "usage_based": 0, "automation": 0, "prolite": count}, Members: members}}, nil
}

type frontendPersonal struct{ identifier string }

func (m frontendPersonal) RefreshPersonal(_ context.Context, material platform.MotherMaterial) (platform.PersonalRefreshResult, error) {
	identifier := material.LoginIdentifier
	if identifier == "" {
		identifier = m.identifier
	}
	expiry := time.Now().UTC().Add(24 * time.Hour)
	claims := map[string]any{"exp": expiry.Unix(), "sub": "auth0|candidate", "email": identifier, "amr": []string{"urn:openai:amr:otp_totp"}, "https://api.openai.com/mfa": map[string]any{"required": true}, "https://api.openai.com/auth": map[string]any{"client_id": "app_X8zY6vW2pQ9tR3dE7nK1jL5gH", "chatgpt_account_id": "personal-account", "chatgpt_user_id": "candidate-user", "chatgpt_plan_type": "free", "scope": "openid email profile offline_access model.request model.read organization.read organization.write"}}
	raw, _ := json.Marshal(claims)
	return platform.PersonalRefreshResult{Status: "ready", Session: platform.PersonalSession{AccessToken: "e30." + base64.RawURLEncoding.EncodeToString(raw) + ".fixture", ExpiresAt: expiry, DeviceID: "fixture-device", Cookies: []platform.SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "mock-cookie"}}}}, nil
}

type frontendExchange struct{}

func (frontendExchange) ExchangeWorkspace(_ context.Context, _ platform.PersonalSession, id string) (platform.WorkspaceAccess, error) {
	return fixtureWorkspaceAccess(id), nil
}

type frontendProbe struct{}

func (frontendProbe) ProbeDestination(context.Context, string, string, string) (ownerapi.DeliveryDestinationTestConnection, ownerapi.DeliveryDestinationTestTarget) {
	return "connected", "connected"
}

func TestFrontendRegisteredOwnerPublicAcceptance(t *testing.T) {
	if os.Getenv("TSW_FRONTEND_BROWSER") != "1" && os.Getenv("TSW_FRONTEND_PREVIEW") != "1" {
		t.Skip("explicit isolated frontend gate required")
	}
	previewMode := os.Getenv("TSW_FRONTEND_PREVIEW") == "1"
	expected := "postgres://postgres@127.0.0.1:55411/tsw_ticket17_worker?sslmode=disable"
	if previewMode {
		expected = "postgres://postgres@127.0.0.1:55411/tsw_ticket17_preview?sslmode=disable"
	}
	if os.Getenv("TSW_TEST_DATABASE_URL") != expected {
		t.Fatal("requires designated disposable loopback database")
	}
	f := newRemovalFixtureWithUsage(t, 1, true)
	ctx := context.Background()
	password := "mock-owner-password-17"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE tsw_owners SET password_hash=$2 WHERE id=$1`, f.owner, hash)
	f.h.dummyPasswordHash = hash
	f.h.secureCookies = true
	// Seal test bootstrap materials at their already-bound revision. Production
	// migration correctly invalidates plaintext-era discovery; this fixture starts
	// with the current encrypted source instead of bypassing that invalidation.
	motherPassword, err := mothersecret.Seal(f.h.keyRing, f.mother, 1, mothersecret.Password, []byte("password"))
	if err != nil {
		t.Fatal(err)
	}
	motherTOTP, err := mothersecret.Seal(f.h.keyRing, f.mother, 1, mothersecret.TOTP, []byte("JBSWY3DPEHPK3PXP"))
	if err != nil {
		t.Fatal(err)
	}
	// Return the prepared selection to its first real API step. No task has run.
	draftResult(t, operationDraftRequest(t, f.h, f.session, f.csrf, "PATCH", map[string]any{"expectedVersion": 1, "choice": "back", "backTo": "mother"}), 200)
	f.h.selectedWorkspaceReader = frontendFacts{f}
	f.h.personalRefresh = frontendPersonal{identifier: f.preview.Candidates[0].Identifier}
	f.h.workspaceTokenExchanger = frontendExchange{}
	f.h.destinationProbe = frontendProbe{}
	f.h.channelDelivery = &channelMock{remote: map[uuid.UUID]ChannelReception{}, pushes: map[uuid.UUID]int{}, final: true, recipient: BatchZIPRecipient{CustomerID: "mock-customer", AuthorizationID: "mock-authorization"}}
	key, nonce, sealed, err := auth.EncryptSecret([]byte("mock-channel-secret"), f.h.keyRing)
	if err != nil {
		t.Fatal(err)
	}
	// Test setup seals the placeholder, without changing any source revision.
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE tsw_delivery_destinations SET secret_key_version=$2,secret_nonce=$3,secret_ciphertext=$4 WHERE id=$1`, f.preview.DestinationId, key, nonce, sealed); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE tsw_mother_account_credentials SET password_secret=$2,totp_secret=$3 WHERE mother_account_id=$1`, f.mother, motherPassword, motherTOTP); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE tsw_mother_accounts SET display_name='演示母号' WHERE id=$1`, f.mother); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE tsw_workspaces SET display_name='演示空间' WHERE id=$1`, f.space); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE tsw_standby_child_batches SET name='演示批次' WHERE id=$1`, f.preview.BatchId); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE tsw_delivery_destinations SET name='演示渠道' WHERE id=$1`, f.preview.DestinationId); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	personal, _ := frontendPersonal{identifier: f.preview.Candidates[0].Identifier}.RefreshPersonal(ctx, platform.MotherMaterial{})
	child := f.preview.Candidates[0].AccountId
	version, n, s, err := sealSessionFor("target", f.h.keyRing, child, 1, personal.Session)
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO tsw_target_personal_access(target_account_id,secret_revision,status) VALUES($1,1,'ready')`, child)
	f.exec(t, `INSERT INTO tsw_target_personal_sessions(target_account_id,secret_revision,attempt,generation,key_version,nonce,sealed_session,expires_at) VALUES($1,1,1,$2,$3,$4,$5,$6)`, child, uuid.New(), version, n, s, personal.Session.ExpiresAt)
	// Twenty-one pool-only accounts allow exact cross-page selection. They are
	// never added to the Workspace, never joined, and never delivered.
	pw, _ := targetdomain.SealMaterial("password", f.h.keyRing)
	totp, _ := targetdomain.SealMaterial("JBSWY3DPEHPK3PXP", f.h.keyRing)
	for i := 0; i < 21; i++ {
		id := uuid.New()
		f.exec(t, `INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,$2,decode($3,'hex'),1,$2)`, id, fmt.Sprintf("pool%02d@selection.test", i), fmt.Sprintf("%064x", 500+i))
		f.exec(t, `INSERT INTO tsw_target_credentials(target_account_id,password_secret,totp_secret,material_status,materials_sealed) VALUES($1,$2,$3,'complete',true)`, id, pw, totp)
	}
	// Existing typed dispatch transport accepts only mock responses; an unexpected
	// request fails this test instead of reaching an external platform.
	d := &dispatchTransport{t: t, f: f, session: personal.Session, role: "owner", remoteWorkspace: f.space.String(), seat: "prolite"}
	d.membershipBody = fmt.Sprintf(`{"complete":true,"members":[{"kind":"member","platform_member_id":"candidate-member","platform_account_user_id":"candidate-user","identifier":%q,"status":"active","seat_type":"prolite"}]}`, f.preview.Candidates[0].Identifier)
	route := &dispatchRoute{client: &http.Client{Timeout: time.Second, Transport: d}}
	f.h.rotationJoinEgress = func(context.Context) (rotationJoinEgress, error) {
		_ = f.pool.QueryRow(ctx, `SELECT id FROM tsw_rotation_released_slots ORDER BY id LIMIT 1`).Scan(&d.slot)
		return route, nil
	}
	f.h.rotationJoinAdapters = officialRotationJoinAdapters
	expiry := time.Now().UTC().Truncate(time.Second).Add(24 * time.Hour)
	jwt := func(plan, scope string) string {
		raw, _ := json.Marshal(map[string]any{"exp": expiry.Unix(), "sub": "auth0|candidate", "scope": scope, "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": f.space.String(), "chatgpt_user_id": "candidate-user", "chatgpt_plan_type": plan}})
		return "e30." + base64.RawURLEncoding.EncodeToString(raw) + ".mock"
	}
	credential := &credentialMock{t: t, f: f, web: platform.WorkspaceAccess{WorkspaceID: f.space.String(), AccessToken: jwt("k12", "organization.read"), DeviceID: "mock-device", SessionID: uuid.NewString(), ExpiresAt: expiry, Cookies: []platform.SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "mock-cookie"}, {Name: "_account", Value: f.space.String()}, {Name: "oai-workspace", Value: f.space.String()}}}, oauth: platform.DeliveryCredentialSet{WorkspaceID: f.space.String(), PlatformSubjectID: "candidate-user", AccessToken: jwt("team", platform.CodexScope), IDToken: jwt("team", platform.CodexScope), RefreshToken: "mock-refresh", ExpiresIn: 86400, Scope: platform.CodexScope}}
	f.h.rotationCredentialAdapters = func(platform.DiscoveryClient) platform.RotationCredentialAdapter {
		credential.slot = d.slot
		return credential
	}
	f.h.rotationUsageAdapters = func(platform.DiscoveryClient) platform.RotationUsageReader {
		return &usageMock{f: f, scope: "workspace", result: "zero"}
	}
	public := NewPublicRedeemHandler(f.pool, f.h.keyRing, nil)
	private := httptest.NewTLSServer(internalapi.Handler(public))
	defer private.Close()
	client := private.Client()
	client.Transport.(*http.Transport).TLSClientConfig.Certificates = []tls.Certificate{{}}
	publicStatic, _ := filepath.Abs("../../web/dist/public")
	gateway, closeGateway, err := NewGatewayHandler(GatewayConfig{ControlURL: private.URL + privateHealthPath, ProbeClient: client, StaticDir: publicStatic})
	if err != nil {
		t.Fatal(err)
	}
	defer closeGateway()
	publicServer := frontendServer(t, gateway, previewMode, "127.0.0.1:18418")
	defer publicServer.Close()
	ownerStatic, _ := filepath.Abs("../../web/dist/owner")
	static, err := newStaticHandler(ownerStatic, ownerRoute)
	if err != nil {
		t.Fatal(err)
	}
	ownerMux := http.NewServeMux()
	ownerMux.Handle("/api/owner/", ownerapi.HandlerWithOptions(f.h, ownerapi.StdHTTPServerOptions{ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
		if isCSRFBindingError(err) {
			writeProblem(w, r, 403, "csrf_rejected", "Forbidden", "Rejected", 0)
			return
		}
		writeProblem(w, r, 400, "invalid_request", "Invalid", "Invalid", 0)
	}}))
	ownerMux.Handle("/", static)
	ownerServer := frontendServer(t, ownerMux, previewMode, "127.0.0.1:18417")
	defer ownerServer.Close()
	f.h.origins, err = auth.ParseOriginPolicy(ownerServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	info := map[string]any{"owner": ownerServer.URL + "/owner/", "public": publicServer.URL + "/redeem/", "username": "owner", "password": password, "platform": "isolated mock only", "database": expected}
	raw, _ := json.MarshalIndent(info, "", "  ")
	if previewMode {
		file := os.Getenv("TSW_PREVIEW_INFO")
		if file == "" {
			t.Fatal("preview info output required")
		}
		if err = os.WriteFile(file, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		t.Log(string(raw))
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
		defer signal.Stop(signals)
		<-signals
		return
	}
	command := exec.Command("node", "../../web/tools/owner-browser.mjs", ownerServer.URL, publicServer.URL, password)
	command.Env = os.Environ()
	output, err := command.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal("registered compiled Owner/Public browser gate", err)
	}
	if f.http.calls != 1 || len(d.paths) != 2 || credential.webCalls != 1 || credential.oauthCalls != 1 || zipCount(t, f) != 1 {
		t.Fatalf("duplicated lifecycle removal=%d join=%v credentials=%d/%d ZIP=%d", f.http.calls, d.paths, credential.webCalls, credential.oauthCalls, zipCount(t, f))
	}
}
func frontendServer(t *testing.T, h http.Handler, fixed bool, address string) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(h)
	if fixed {
		s.Listener.Close()
		listener, err := net.Listen("tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		s.Listener = listener
	}
	s.StartTLS()
	if !strings.HasPrefix(s.URL, "https://127.0.0.1:") {
		t.Fatal("preview must remain loopback")
	}
	return s
}
