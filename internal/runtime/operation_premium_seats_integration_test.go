//go:build integration

package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/accountsession"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/egress"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
	targetdomain "github.com/xft0202/Apophis-TeamSeatWatch/internal/target"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/task"
)

type premiumFixture struct {
	h                                *OwnerAuthHandler
	batch, mother, workspace, target uuid.UUID
	verification                     int64
	session, csrf                    string
	motherToken                      string
}

func premiumOperationFixture(t *testing.T) premiumFixture {
	return premiumOperationFixtureWithCapacity(t, 200, 9, 99)
}

func premiumOperationFixtureWithCapacity(t *testing.T, opened, occupied, pending int) premiumFixture {
	t.Helper()
	pool, h, _, session, csrf := childReviewFixture(t)
	ctx := context.Background()
	f := premiumFixture{h: h, batch: uuid.New(), mother: uuid.New(), workspace: uuid.New(), target: uuid.New(), session: session, csrf: csrf}
	run, generation, exchange, binding := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO tsw_mother_accounts(id,display_name) VALUES($1,'scoped mother')`, f.mother)
	exec(`INSERT INTO tsw_mother_account_credentials(mother_account_id,login_identifier,identifier_hmac,identifier_key_version,password_secret,totp_secret) VALUES($1,'mother@premium.test',decode(repeat('a1',32),'hex'),1,'pw','totp')`, f.mother)
	exec(`INSERT INTO tsw_workspaces(id,display_name,platform_workspace_id) VALUES($1::uuid,'premium workspace',$1::uuid::text)`, f.workspace)
	exec(`INSERT INTO tsw_workspace_projections(workspace_id) VALUES($1)`, f.workspace)
	exec(`INSERT INTO tsw_mother_workspace_bindings(id,mother_account_id,workspace_id) VALUES($1,$2,$3)`, binding, f.mother, f.workspace)
	exec(`INSERT INTO tsw_mother_personal_sessions(mother_account_id,secret_revision,generation,key_version,nonce,sealed_session,expires_at) VALUES($1,1,$2,1,decode(repeat('11',12),'hex'),decode(repeat('22',32),'hex'),now()+interval '1 day')`, f.mother, generation)
	exec(`INSERT INTO tsw_mother_discoveries(mother_account_id,run_id,secret_revision,session_generation,status) VALUES($1,$2,1,$3,'discovered')`, f.mother, run, generation)
	exec(`INSERT INTO tsw_mother_workspace_visibility(mother_account_id,workspace_id,run_id,access_status) VALUES($1,$2,$3,'readable')`, f.mother, f.workspace, run)
	access := fixtureWorkspaceAccess(f.workspace.String())
	f.motherToken = access.AccessToken
	key, nonce, sealed, err := sealWorkspaceAccess(h.keyRing, workspaceAccessBinding{motherID: f.mother, workspaceID: f.workspace, run: run, generation: generation, revision: 1, exchangeID: exchange, attempt: 1}, access)
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO tsw_selected_workspace_tokens(mother_account_id,workspace_id,discovery_run_id,session_generation,secret_revision,exchange_id,status,key_version,nonce,sealed_access,expires_at) VALUES($1,$2,$3,$4,1,$5,'ready',$6,$7,$8,$9)`, f.mother, f.workspace, run, generation, exchange, key, nonce, sealed, time.Now().Add(30*time.Minute))
	err = pool.QueryRow(ctx, `INSERT INTO tsw_workspace_verifications(workspace_id,mother_account_id,discovery_run_id,session_generation,secret_revision,token_attempt,token_exchange_id,source,outcome,permission,completeness,observed_at,expires_at,active_until,seat_limit,member_count,pending_invite_count,seat_type_counts,seat_entitlements) VALUES($1,$2,$3,$4,1,1,$5,'injected_platform_reader','verified','read','complete',now(),now()+interval '1 day',now()+interval '1 day',2,$6+2,$7,jsonb_build_object('default',2,'prolite',$6::int,'usage_based',0,'automation',0),jsonb_build_object('default',2,'prolite',$8::int)) RETURNING id`, f.workspace, f.mother, run, generation, exchange, occupied, pending, opened).Scan(&f.verification)
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO tsw_workspace_verification_entries(verification_id,kind,identifier,identifier_hmac,identifier_key_version,status,role,platform_member_id,seat_type) SELECT $1,'member',CASE n WHEN 1 THEN 'mother@premium.test' ELSE 'member'||n||'@premium.test' END,digest('member'||n,'sha256'),1,'listed',CASE n WHEN 1 THEN 'account-owner' ELSE 'standard-user' END,'member-'||n,CASE WHEN n<=2 THEN 'default' ELSE 'prolite' END FROM generate_series(1,$2::int+2) n`, f.verification, occupied)
	exec(`INSERT INTO tsw_workspace_verification_entries(verification_id,kind,identifier,identifier_hmac,identifier_key_version,status,seat_type) SELECT $1,'pending_invite','invite'||n||'@premium.test',digest('invite'||n,'sha256'),1,'pending','prolite' FROM generate_series(1,$2::int) n`, f.verification, pending)
	password, err := targetdomain.SealMaterial("password", h.keyRing)
	if err != nil {
		t.Fatal(err)
	}
	totp, err := targetdomain.SealMaterial("JBSWY3DPEHPK3PXP", h.keyRing)
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,'candidate@premium.test',decode(repeat('b1',32),'hex'),1,'candidate')`, f.target)
	exec(`INSERT INTO tsw_target_credentials(target_account_id,password_secret,totp_secret,material_status,materials_sealed) VALUES($1,$2,$3,'complete',true)`, f.target, password, totp)
	exec(`INSERT INTO tsw_batches(id,binding_id,sequence_no,status,planned_at) VALUES($1,$2,1,'planned',now()+interval '1 day')`, f.batch, binding)
	exec(`INSERT INTO tsw_batch_targets(batch_id,target_account_id,ordinal) VALUES($1,$2,1)`, f.batch, f.target)
	h.selectedWorkspaceReader = &selectedFixture{}
	return f
}

func TestPremiumOperationUsesScopedFactsAndSeatClasses(t *testing.T) {
	f := premiumOperationFixture(t)
	ctx := context.Background()
	request := httptest.NewRequest("GET", "/preview", nil)
	preview, err := f.h.joinPreview(request, f.batch.String())
	if err != nil || !preview.CanProceed || preview.TargetSeatType != "prolite" || preview.PaidDefaultSeats == nil || *preview.PaidDefaultSeats != 2 {
		t.Fatalf("scoped preview: %+v %v", preview, err)
	}
	facts, err := f.h.selectedWorkspacePage(ctx, f.workspace, f.mother, 1, 20, "member")
	if err != nil || facts.MemberSeatTypeCounts == nil || (*facts.MemberSeatTypeCounts)["prolite"] != 9 || facts.PendingInviteSeatTypeCounts == nil || (*facts.PendingInviteSeatTypeCounts)["prolite"] != 99 {
		t.Fatalf("seat summary: %+v %v", facts, err)
	}
	for _, entry := range facts.Members {
		if entry.SeatType == nil {
			t.Fatal("member API lost saved seat type")
		}
	}
	// A new immutable snapshot may reveal a conflicting candidate seat. It
	// must block the whole frozen scope, without using ordinary entitlement.
	for _, seat := range []any{"default", nil} {
		var next int64
		err = f.h.pool.QueryRow(ctx, `INSERT INTO tsw_workspace_verifications(workspace_id,mother_account_id,discovery_run_id,session_generation,secret_revision,token_attempt,token_exchange_id,source,outcome,permission,completeness,observed_at,expires_at,active_until,seat_limit,member_count,pending_invite_count,seat_type_counts) SELECT workspace_id,mother_account_id,discovery_run_id,session_generation,secret_revision,token_attempt,token_exchange_id,source,outcome,permission,completeness,now(),now()+interval '1 day',active_until,seat_limit,member_count,pending_invite_count,seat_type_counts FROM tsw_workspace_verifications WHERE id=$1 RETURNING id`, f.verification).Scan(&next)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.h.pool.Exec(ctx, `INSERT INTO tsw_workspace_verification_entries(verification_id,kind,identifier,identifier_hmac,identifier_key_version,status,role,platform_member_id,seat_type) SELECT $1,kind,identifier,identifier_hmac,identifier_key_version,status,role,platform_member_id,seat_type FROM tsw_workspace_verification_entries WHERE verification_id=$2`, next, f.verification)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.h.pool.Exec(ctx, `INSERT INTO tsw_workspace_verification_entries(verification_id,kind,identifier,identifier_hmac,identifier_key_version,status,seat_type) VALUES($1,'pending_invite','candidate@premium.test',decode(repeat('c1',32),'hex'),1,'pending',$2)`, next, seat)
		if err != nil {
			t.Fatal(err)
		}
		preview, err = f.h.joinPreview(request, f.batch.String())
		if err != nil || preview.CanProceed {
			t.Fatalf("wrong seat admitted: %+v %v", preview, err)
		}
		found := false
		for _, blocker := range preview.Blockers {
			found = found || blocker.Code == "target_seat_mismatch"
		}
		if !found {
			t.Fatal("missing seat-specific blocker")
		}
	}
}

func TestPremiumOperationRejectsUnavailableFacts(t *testing.T) {
	for _, scenario := range []string{"stale", "partial", "no management", "wrong mother"} {
		t.Run(scenario, func(t *testing.T) {
			f := premiumOperationFixture(t)
			ctx := context.Background()
			if scenario == "wrong mother" {
				if _, err := f.h.selectedWorkspacePage(ctx, f.workspace, uuid.New(), 1, 20, "member"); err == nil {
					t.Fatal("another mother read the scoped snapshot")
				}
				return
			}
			var next int64
			err := f.h.pool.QueryRow(ctx, `INSERT INTO tsw_workspace_verifications(workspace_id,mother_account_id,discovery_run_id,session_generation,secret_revision,token_attempt,token_exchange_id,source,outcome,permission,completeness,observed_at,expires_at,active_until,seat_limit,member_count,pending_invite_count,seat_type_counts)
			SELECT workspace_id,mother_account_id,discovery_run_id,session_generation,secret_revision,token_attempt,token_exchange_id,source,
			CASE WHEN $2='partial' THEN 'partial' ELSE outcome END,permission,CASE WHEN $2='partial' THEN 'partial' ELSE completeness END,
			CASE WHEN $2='stale' THEN now()-interval '2 minutes' ELSE now() END,
			CASE WHEN $2='stale' THEN now()-interval '1 minute' ELSE now()+interval '1 day' END,
			CASE WHEN $2='partial' THEN NULL ELSE active_until END,CASE WHEN $2='partial' THEN NULL ELSE seat_limit END,
			CASE WHEN $2='partial' THEN NULL ELSE member_count END,CASE WHEN $2='partial' THEN NULL ELSE pending_invite_count END,seat_type_counts
			FROM tsw_workspace_verifications WHERE id=$1 RETURNING id`, f.verification, scenario).Scan(&next)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.h.pool.Exec(ctx, `INSERT INTO tsw_workspace_verification_entries(verification_id,kind,identifier,identifier_hmac,identifier_key_version,status,role,platform_member_id,seat_type)
			SELECT $1,kind,identifier,identifier_hmac,identifier_key_version,status,CASE WHEN $3='no management' THEN 'standard-user' ELSE role END,platform_member_id,seat_type
			FROM tsw_workspace_verification_entries WHERE verification_id=$2`, next, f.verification, scenario)
			if err != nil {
				t.Fatal(err)
			}
			preview, err := f.h.joinPreview(httptest.NewRequest("GET", "/preview", nil), f.batch.String())
			if err != nil || preview.CanProceed || len(preview.Blockers) == 0 {
				t.Fatalf("unavailable facts admitted: %+v %v", preview, err)
			}
		})
	}
}

type operationOAuthFixture struct {
	create func(platform.DeliveryCredentialRequest) (platform.DeliveryCredentialSet, error)
	probe  func() (platform.DeliveryLiveness, error)
}

func (f operationOAuthFixture) CreateDeliveryCredentials(_ context.Context, input platform.DeliveryCredentialRequest) (platform.DeliveryCredentialSet, error) {
	return f.create(input)
}
func (f operationOAuthFixture) CheckDeliveryLiveness(context.Context, string, string) (platform.DeliveryLiveness, error) {
	return f.probe()
}

func TestOperationInvitationThenWorkspaceOAuth(t *testing.T) {
	for _, scenario := range []string{"premium", "hidden-member", "wrong-workspace", "oauth-failed", "historical-marker", "wrong-account", "workspace-unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			f := premiumOperationFixture(t)
			ctx := context.Background()
			claims, _ := json.Marshal(map[string]any{"email": "candidate@premium.test", "exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]any{"client_id": "app_X8zY6vW2pQ9tR3dE7nK1jL5gH", "chatgpt_account_id": "child-personal", "chatgpt_user_id": "child-member", "chatgpt_plan_type": "free", "scp": strings.Fields("openid email profile offline_access model.request model.read organization.read organization.write"), "amr": []string{"urn:openai:amr:otp_totp"}}, "https://api.openai.com/mfa": map[string]any{"required": "yes"}})
			childToken := "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
			saved := platform.PersonalSession{AccessToken: childToken, DeviceID: "child-device", ExpiresAt: time.Now().Add(time.Hour), Cookies: []platform.SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "child-cookie"}}}
			key, nonce, sealed, err := accountsession.Seal("target", f.h.keyRing, f.target, 1, saved)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.h.pool.Exec(ctx, `INSERT INTO tsw_target_personal_access(target_account_id,secret_revision,status) VALUES($1,1,'ready');`, f.target)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.h.pool.Exec(ctx, `INSERT INTO tsw_target_personal_sessions(target_account_id,secret_revision,attempt,generation,key_version,nonce,sealed_session,expires_at) VALUES($1,1,1,$2,$3,$4,$5,$6)`, f.target, uuid.New(), key, nonce, sealed, saved.ExpiresAt)
			if err != nil {
				t.Fatal(err)
			}
			payload, _ := json.Marshal(map[string]any{"idempotencyKey": "premium-fixture-operation", "confirm": true})
			req := httptest.NewRequest("POST", "/join", bytes.NewReader(payload))
			req.SetPathValue("batchId", f.batch.String())
			req.Header.Set("Origin", "https://owner.test")
			req.Header.Set(auth.CSRFHeaderName, f.csrf)
			req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: f.csrf})
			req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: f.session})
			result := httptest.NewRecorder()
			f.h.createJoinOperation(result, req)
			if result.Code != 202 {
				t.Fatalf("create %d %s", result.Code, result.Body.String())
			}

			invited, loggedIn := false, false
			grants, childReads := 0, 0
			var mutations []string
			transport := selectedRoundTrip(func(request *http.Request) (*http.Response, error) {
				body := "{}"
				path := request.URL.Path
				switch {
				case request.Method == "GET" && path == "/backend-api/subscriptions":
					body = `{"active_until":"2027-11-02T00:00:00Z","seats_in_use":2,"seats_entitled":202,"seat_capacity":[{"type":"default","paid":2},{"type":"prolite","paid":200}]}`
				case request.Method == "GET" && strings.HasSuffix(path, "/seat_type_counts"):
					body = `{"seat_type_counts":{"default":2,"prolite":0,"automation":0,"usage_based":0}}`
				case request.Method == "GET" && path == "/backend-api/wham/usage":
					childReads++
					body = `{"rate_limit":{"allowed":true}}`
					if request.Header.Get("Authorization") != "Bearer "+childToken {
						t.Fatal("wrong Personal identity")
					}
				case request.Method == "GET" && strings.HasSuffix(path, "/users"):
					items := `{"id":"mother-member","email":"mother@premium.test","role":"account-owner","seat_type":"default"}`
					total := 1
					if loggedIn {
						total++
						if scenario == "hidden-member" {
							items += `,{"id":"child-member","role":"standard-user","seat_type":"prolite"}`
						} else {
							items += `,{"id":"child-member","email":"candidate@premium.test","role":"standard-user","seat_type":"prolite"}`
						}
					}
					body = fmt.Sprintf(`{"total":%d,"limit":100,"offset":0,"items":[%s]}`, total, items)
				case request.Method == "GET" && strings.HasSuffix(path, "/invites"):
					items := ""
					total := 0
					if invited && !loggedIn {
						items = `{"email_address":"candidate@premium.test","status":2,"seat_type":"prolite"}`
						total = 1
					}
					body = fmt.Sprintf(`{"total":%d,"limit":100,"offset":0,"items":[%s]}`, total, items)
				case request.Method == "POST" && strings.HasSuffix(path, "/invites"):
					mutations = append(mutations, path)
					if request.Header.Get("Authorization") != "Bearer "+f.motherToken {
						t.Fatal("invitation did not use mother")
					}
					var payload map[string]any
					if json.NewDecoder(request.Body).Decode(&payload) != nil || payload["seat_type"] != "prolite" {
						t.Fatal("wrong invitation seat")
					}
					invited = true
					body = `{"account_invites":[{"email_address":"candidate@premium.test"}]}`
				default:
					t.Fatalf("unexpected external request %s %s", request.Method, path)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})
			router, err := egress.New(egress.Config{Mode: egress.ModeDirect})
			if err != nil {
				t.Fatal(err)
			}
			defer router.CloseIdleConnections()
			leases, err := egress.NewLeaseManager(router, egress.Admission{})
			if err != nil {
				t.Fatal(err)
			}
			worker := task.Worker{Store: task.NewStore(f.h.pool, f.h.keyRing), Egress: leases, ID: "workspace-oauth-fixture", Joiner: func(ctx context.Context, client *http.Client, target task.JoinTarget) (platform.Joiner, error) {
				client.Transport = transport
				return f.h.operationJoiner(ctx, client, target)
			}, DeliveryAdapter: func(client *http.Client, _ platform.Credentials) (platform.DeliveryAdapter, error) {
				client.Transport = transport
				return operationOAuthFixture{create: func(input platform.DeliveryCredentialRequest) (platform.DeliveryCredentialSet, error) {
					grants++
					if !invited || input.Workspace != f.workspace.String() || input.Identifier != "candidate@premium.test" || input.PersonalSession == nil {
						t.Fatal("OAuth not bound to the invited account and exact workspace")
					}
					if scenario == "workspace-unavailable" {
						return platform.DeliveryCredentialSet{}, &platform.OAuthFailure{Code: "oauth_workspace_unavailable", HTTPStatus: 401}
					}
					if scenario == "oauth-failed" {
						return platform.DeliveryCredentialSet{}, errors.New("simulated OAuth failure")
					}
					workspace := f.workspace.String()
					subject := "child-member"
					if scenario == "wrong-account" {
						subject = "another-member"
					}
					if scenario == "wrong-workspace" {
						workspace = uuid.NewString()
					}
					loggedIn = true
					return platform.DeliveryCredentialSet{RefreshToken: "refresh", AccessToken: "access", IDToken: "id", PlatformSubjectID: subject, WorkspaceID: workspace, ExpiresIn: 3600, Scope: "openid offline_access"}, nil
				}, probe: func() (platform.DeliveryLiveness, error) {
					return platform.DeliveryLiveness{Status: "ok", HTTPStatus: 200, PlatformSubjectID: "child-member", WorkspaceID: f.workspace.String(), ObservedAt: time.Now()}, nil
				}}, nil
			}}
			if processed, err := worker.RunOnce(ctx); !processed || err != nil {
				t.Fatalf("invite worker: %v %v", processed, err)
			}
			operation, err := f.h.joinOperationByBatch(httptest.NewRequest("GET", "/operation", nil), f.batch.String(), 1, 20)
			if err != nil || operation.Status != "awaiting_login" || operation.InvitationConfirmedCount != 1 || operation.SucceededCount != 0 || grants != 0 || childReads != 0 || len(mutations) != 1 {
				t.Fatalf("step 2 must stop after mother invitation: %+v %v grants=%d childReads=%d mutations=%v", operation, err, grants, childReads, mutations)
			}
			login := func() *httptest.ResponseRecorder {
				payload, _ := json.Marshal(map[string]any{"idempotencyKey": "explicit-workspace-oauth", "concurrency": 2})
				request := httptest.NewRequest("POST", "/login", bytes.NewReader(payload))
				request.Header.Set("Origin", "https://owner.test")
				request.Header.Set(auth.CSRFHeaderName, f.csrf)
				request.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: f.csrf})
				request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: f.session})
				response := httptest.NewRecorder()
				f.h.StartBatchAccountLogin(response, request, f.batch, ownerapi.StartBatchAccountLoginParams{})
				return response
			}
			if scenario == "historical-marker" {
				_, err = f.h.pool.Exec(ctx, `UPDATE tsw_operation_targets SET platform_request_stage='accept_join' WHERE status='invited'`)
				if err != nil {
					t.Fatal(err)
				}
			}
			response := login()
			if response.Code != 202 {
				t.Fatalf("OAuth start: %d %s", response.Code, response.Body.String())
			}
			response = login()
			var replay ownerapi.ProbeDeliveryResponse
			json.Unmarshal(response.Body.Bytes(), &replay)
			if response.Code != 202 || replay.Queued != 0 {
				t.Fatal("duplicate OAuth job queued")
			}
			processed, oauthErr := worker.RunOnce(ctx)
			if !processed || (oauthErr != nil && scenario != "oauth-failed" && scenario != "wrong-workspace" && scenario != "wrong-account" && scenario != "workspace-unavailable") {
				t.Fatalf("OAuth worker: %v %v", processed, oauthErr)
			}
			operation, err = f.h.joinOperationByBatch(httptest.NewRequest("GET", "/operation", nil), f.batch.String(), 1, 20)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "workspace-unavailable" {
				if operation.Targets[0].DiagnosticCode == nil || *operation.Targets[0].DiagnosticCode != "oauth_workspace_unavailable" {
					t.Fatal("API lost exact platform denial")
				}
				var followups int
				f.h.pool.QueryRow(ctx, `SELECT count(*) FROM tsw_tasks WHERE task_type='join_reconcile'`).Scan(&followups)
				if followups != 0 {
					t.Fatal("definite OAuth rejection queued unnecessary member reconciliation")
				}
			}
			success := scenario == "premium" || scenario == "hidden-member" || scenario == "historical-marker"
			if grants != 1 || len(mutations) != 1 || childReads != 0 {
				t.Fatalf("OAuth flow issued extra invitation/recipient API: grants=%d writes=%v", grants, mutations)
			}
			if success {
				if operation.SucceededCount != 1 || operation.Targets[0].Delivery == nil || operation.Targets[0].Delivery.Status != "ready" {
					t.Fatalf("OAuth result not saved: %+v", operation)
				}
				var versions, extra int
				f.h.pool.QueryRow(ctx, `SELECT count(*) FROM tsw_delivery_versions v JOIN tsw_oauth_assets a ON a.id=v.oauth_asset_id JOIN tsw_batch_memberships m ON m.id=a.membership_id WHERE m.batch_id=$1`, f.batch).Scan(&versions)
				f.h.pool.QueryRow(ctx, `SELECT count(*) FROM tsw_tasks WHERE task_type='oauth_generate'`).Scan(&extra)
				if versions != 1 || extra != 0 {
					t.Fatalf("OAuth login was repeated after publication: versions=%d extra=%d", versions, extra)
				}
			} else if operation.SucceededCount != 0 || operation.BlockedCount != 1 {
				t.Fatalf("failed or wrong-workspace OAuth counted as success: %+v", operation)
			}
		})
	}
}

func TestIndependentOperationDoesNotWaitForOtherBatch(t *testing.T) {
	f := premiumOperationFixture(t)
	ctx := context.Background()
	original := uuid.New()
	_, err := f.h.pool.Exec(ctx, `INSERT INTO tsw_batches(id,binding_id,sequence_no,status,planned_at) SELECT $1,binding_id,2,'joining',planned_at FROM tsw_batches WHERE id=$2`, original, f.batch)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.h.pool.Exec(ctx, `INSERT INTO tsw_operations(owner_id,workspace_id,batch_id,operation_type,idempotency_key,request_hash,input_snapshot,correlation_id,status,completed_at) SELECT id,$1,$2,'join','original-blocked-operation',decode(repeat('11',32),'hex'),'{}','fixture','blocked',now() FROM tsw_owners LIMIT 1`, f.workspace, original)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.h.joinPreview(httptest.NewRequest("GET", "/preview", nil), f.batch.String())
	if err != nil || !preview.CanProceed || preview.ActiveBatchId != nil {
		t.Fatalf("independent accounts are blocked by unrelated batch: %+v %v", preview, err)
	}
	payload, _ := json.Marshal(map[string]any{"idempotencyKey": "independent-new-operation", "confirm": true})
	request := httptest.NewRequest("POST", "/join", bytes.NewReader(payload))
	request.SetPathValue("batchId", f.batch.String())
	request.Header.Set("Origin", "https://owner.test")
	request.Header.Set(auth.CSRFHeaderName, f.csrf)
	request.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: f.csrf})
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: f.session})
	response := httptest.NewRecorder()
	f.h.createJoinOperation(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("independent invitation rejected: %d %s", response.Code, response.Body.String())
	}
	var oldStatus string
	if err = f.h.pool.QueryRow(ctx, `SELECT status FROM tsw_operations WHERE batch_id=$1`, original).Scan(&oldStatus); err != nil || oldStatus != "blocked" {
		t.Fatalf("original result overwritten: %s %v", oldStatus, err)
	}
	var active int
	f.h.pool.QueryRow(ctx, `SELECT count(*) FROM tsw_batches WHERE status='joining'`).Scan(&active)
	if active != 2 {
		t.Fatalf("independent batches not retained: %d", active)
	}
	retry := httptest.NewRequest("POST", "/join", bytes.NewReader(payload))
	retry.SetPathValue("batchId", f.batch.String())
	retry.Header = request.Header.Clone()
	retried := httptest.NewRecorder()
	f.h.createJoinOperation(retried, retry)
	if retried.Code != http.StatusAccepted {
		t.Fatalf("same request did not restore original: %d %s", retried.Code, retried.Body.String())
	}
	var operations, tasks int
	f.h.pool.QueryRow(ctx, `SELECT count(*) FROM tsw_operations WHERE batch_id=$1`, f.batch).Scan(&operations)
	f.h.pool.QueryRow(ctx, `SELECT count(*) FROM tsw_tasks WHERE task_type='join'`).Scan(&tasks)
	if operations != 1 || tasks != 1 {
		t.Fatalf("retry duplicated execution: operations=%d tasks=%d", operations, tasks)
	}
}

func TestOperationConflictBelongsToSelectedAccount(t *testing.T) {
	for _, scenario := range []struct {
		name, status, memberState string
		uncertain, conflict       bool
	}{
		{"pending-invitation", "invited", "", false, true},
		{"active-member", "succeeded", "active", false, true},
		{"removed-member", "succeeded", "removed", false, false},
		{"uncertain-invitation", "unknown", "", true, true},
		{"failed-without-request", "failed", "", false, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := premiumOperationFixture(t)
			ctx := context.Background()
			original, operation, target := uuid.New(), uuid.New(), uuid.New()
			exec := func(sql string, args ...any) {
				t.Helper()
				if _, err := f.h.pool.Exec(ctx, sql, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec(`INSERT INTO tsw_batches(id,binding_id,sequence_no,status,planned_at) SELECT $1,binding_id,2,'joining',planned_at FROM tsw_batches WHERE id=$2`, original, f.batch)
			exec(`INSERT INTO tsw_operations(id,owner_id,workspace_id,batch_id,operation_type,idempotency_key,request_hash,input_snapshot,correlation_id,status,completed_at) SELECT $1,id,$2,$3,'join','account-scoped-original',decode(repeat('11',32),'hex'),'{}','fixture','blocked',now() FROM tsw_owners LIMIT 1`, operation, f.workspace, original)
			exec(`INSERT INTO tsw_operation_targets(id,operation_id,target_account_id,ordinal,status,completed_at,invitation_confirmed_at,platform_request_may_have_reached,platform_request_stage,platform_request_started_at) VALUES($1,$2,$3,1,$4,CASE WHEN $4='invited' THEN NULL ELSE now() END,CASE WHEN $4='invited' THEN now() ELSE NULL END,$5,CASE WHEN $5 THEN 'send_invitation' ELSE NULL END,CASE WHEN $5 THEN now() ELSE NULL END)`, target, operation, f.target, scenario.status, scenario.uncertain)
			if scenario.memberState != "" {
				var membership uuid.UUID
				err := f.h.pool.QueryRow(ctx, `INSERT INTO tsw_batch_memberships(batch_id,target_account_id,join_operation_target_id,platform_member_id,seat_type,joined_at,state,removed_at) VALUES($1,$2,$3,'existing-premium-member','prolite',now()-interval '1 minute',$4,CASE WHEN $4='removed' THEN now() ELSE NULL END) RETURNING id`, original, f.target, target, scenario.memberState).Scan(&membership)
				if err != nil {
					t.Fatal(err)
				}
				exec(`UPDATE tsw_operation_targets SET target_account_id=NULL,membership_id=$2 WHERE id=$1`, target, membership)
			}
			preview, err := f.h.joinPreview(httptest.NewRequest("GET", "/preview", nil), f.batch.String())
			if err != nil || preview.CanProceed == scenario.conflict {
				t.Fatalf("wrong account conflict: %+v %v", preview, err)
			}
			if scenario.conflict && (preview.ActiveBatchId == nil || *preview.ActiveBatchId != original || len(preview.Blockers) != 1 || !strings.Contains(preview.Blockers[0].Message, "1 个")) {
				t.Fatalf("missing exact original/account scope: %+v", preview)
			}
		})
	}
}

func TestOperationMixedInvitationResultsKeepBoundary(t *testing.T) {
	f := premiumOperationFixture(t)
	ctx := context.Background()
	op := uuid.New()
	second := uuid.New()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.h.pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,'second@premium.test',decode(repeat('c2',32),'hex'),1,'second')`, second)
	exec(`INSERT INTO tsw_target_credentials(target_account_id,password_secret,totp_secret,material_status,materials_sealed) SELECT $1,password_secret,totp_secret,material_status,materials_sealed FROM tsw_target_credentials WHERE target_account_id=$2`, second, f.target)
	exec(`INSERT INTO tsw_batch_targets(batch_id,target_account_id,ordinal) VALUES($1,$2,2)`, f.batch, second)
	exec(`INSERT INTO tsw_operations(id,owner_id,workspace_id,batch_id,operation_type,idempotency_key,request_hash,input_snapshot,correlation_id) SELECT $1,id,$2,$3,'join','mixed-invitation-operation',decode(repeat('11',32),'hex'),jsonb_build_object('seat_type','prolite','verification_id',$4::bigint),'mixed' FROM tsw_owners LIMIT 1`, op, f.workspace, f.batch, f.verification)
	for i, account := range []uuid.UUID{f.target, second} {
		tid := uuid.New()
		exec(`INSERT INTO tsw_operation_targets(id,operation_id,target_account_id,ordinal) VALUES($1,$2,$3,$4)`, tid, op, account, i+1)
		exec(`INSERT INTO tsw_tasks(operation_target_id,workspace_id,target_account_id,task_type,dedupe_key,input_snapshot,correlation_id,max_attempts) VALUES($1,$2,$3,'join',$4,'{}','mixed',3)`, tid, f.workspace, account, "mixed:"+tid.String())
	}
	exec(`UPDATE tsw_batches SET status='joining' WHERE id=$1`, f.batch)
	store := task.NewStore(f.h.pool, f.h.keyRing)
	first, err := store.ClaimJoin(ctx, "mixed", time.Minute, task.AttemptRoute{Mode: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := store.JoinTarget(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.MarkJoinSideEffectStarted(ctx, first, "send_invitation"); err != nil {
		t.Fatal(err)
	}
	if err = store.MarkJoinSideEffectStarted(ctx, first, "request_join"); err == nil {
		t.Fatalf("recipient write allowed before step 3: %v", err)
	}
	if err = store.FinishInvitation(ctx, first, target); err != nil {
		t.Fatal(err)
	}
	next, err := store.ClaimJoin(ctx, "mixed", time.Minute, task.AttemptRoute{Mode: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	target, err = store.JoinTarget(ctx, next)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.FinishJoin(ctx, next, target, "failed", "credential_invalid", "credential_invalid", "preflight", nil); err != nil {
		t.Fatal(err)
	}
	result, err := f.h.joinOperationByBatch(httptest.NewRequest("GET", "/operation", nil), f.batch.String(), 1, 20)
	if err != nil || result.InvitationConfirmedCount != 1 || result.SucceededCount != 0 || result.FailedCount != 1 || result.Status != "failed" || result.CompletedAt == nil {
		t.Fatalf("mixed invitation result: %+v %v", result, err)
	}
	for _, target := range result.Targets {
		if target.Identifier == "" || target.Delivery != nil {
			t.Fatalf("unfinished member disappeared or got delivery: %+v", target)
		}
	}
}
