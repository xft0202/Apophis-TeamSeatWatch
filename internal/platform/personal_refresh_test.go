package platform

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func personalFixtureToken(now time.Time, alter func(map[string]any)) string {
	claims := map[string]any{"exp": now.Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]any{"client_id": personalWebClientID, "chatgpt_account_id": "personal-1", "chatgpt_plan_type": "free", "scp": strings.Fields(personalWebScopes), "amr": []string{"urn:openai:amr:otp_totp"}}, "https://api.openai.com/mfa": map[string]any{"required": "yes"}}
	if alter != nil {
		alter(claims)
	}
	body, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + base64.RawURLEncoding.EncodeToString(body) + ".signature"
}

type personalFlowFixture struct {
	token          string
	signin         string
	redirect       string
	mfaContinue    string
	mfaStatus      int
	passwordStatus int
	paths          []string
	blocked        bool
}

func (f *personalFlowFixture) RoundTrip(req *http.Request) (*http.Response, error) {
	path := req.URL.Host + req.URL.Path
	f.paths = append(f.paths, req.Method+" "+path)
	respond := func(status int, body string, headers http.Header) (*http.Response, error) {
		if headers == nil {
			headers = make(http.Header)
		}
		return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	}
	switch path {
	case "chatgpt.com/api/auth/providers":
		return respond(200, `{}`, nil)
	case "chatgpt.com/api/auth/csrf":
		return respond(200, `{"csrfToken":"csrf"}`, nil)
	case "chatgpt.com/api/auth/signin/openai":
		if f.signin != "" {
			return respond(200, `{"url":"`+f.signin+`"}`, nil)
		}
		return respond(200, `{"url":"https://auth.openai.com/login/start"}`, nil)
	case "auth.openai.com/login/start":
		if f.redirect != "" {
			return respond(302, "", http.Header{"Location": []string{f.redirect}})
		}
		return respond(302, "", http.Header{"Location": []string{"https://auth.openai.com/log-in/password"}})
	case "auth.openai.com/log-in/password":
		return respond(200, `<div>log-in/password</div>`, nil)
	case "auth.openai.com/api/accounts/password/verify":
		if f.passwordStatus != 0 {
			return respond(f.passwordStatus, `{}`, nil)
		}
		return respond(200, `{"continue_url":"https://auth.openai.com/after-password"}`, nil)
	case "auth.openai.com/after-password":
		return respond(200, `/mfa-challenge/challenge-12345678`, nil)
	case "auth.openai.com/api/accounts/mfa/verify":
		body, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(body), `"id":"challenge-12345678"`) || !strings.Contains(string(body), `"type":"totp"`) {
			f.blocked = true
		}
		if f.mfaStatus != 0 {
			return respond(f.mfaStatus, `{}`, nil)
		}
		if f.mfaContinue != "" {
			return respond(200, `{"continue_url":"`+f.mfaContinue+`"}`, nil)
		}
		return respond(200, `{"continue_url":"https://auth.openai.com/after-totp"}`, nil)
	case "auth.openai.com/after-totp":
		return respond(200, `authenticated`, nil)
	case "chatgpt.com/api/auth/session":
		return respond(200, `{"accessToken":"`+f.token+`","expires":"`+time.Now().Add(2*time.Hour).UTC().Format(time.RFC3339)+`"}`, http.Header{"Set-Cookie": []string{"__Secure-next-auth.session-token=cookie-secret; Secure; Path=/"}})
	default:
		f.blocked = true
		return respond(404, `{}`, nil)
	}
}

func TestPersonalWebRefreshUsesOnlyPasswordTOTPAndSessionGET(t *testing.T) {
	fixture := &personalFlowFixture{token: personalFixtureToken(time.Now(), nil)}
	client := &http.Client{Transport: fixture, Timeout: time.Second}
	leases, releases := 0, 0
	adapter := PersonalWebRefresher{Client: func(context.Context) (*http.Client, func(), error) {
		leases++
		return client, func() { releases++ }, nil
	}}
	result, err := adapter.RefreshPersonal(t.Context(), MotherMaterial{LoginIdentifier: "mother@example.test", Password: "password", TOTPSecret: "JBSWY3DPEHPK3PXP"})
	if err != nil || result.Status != "ready" || result.Session.AccessToken != fixture.token || result.Session.DeviceID == "" || len(result.Session.Cookies) != 1 || leases != 1 || releases != 1 || fixture.blocked {
		t.Fatalf("result=%+v err=%v requests=%v lease=%d/%d", result, err, fixture.paths, leases, releases)
	}
	for _, path := range fixture.paths {
		if strings.Contains(path, "workspace/select") || strings.Contains(path, "oauth/") || strings.Contains(path, "accounts/check") {
			t.Fatalf("forbidden operation: %s", path)
		}
	}
}

func TestPostTOTPPersonalExpiryRequiresPositivePersonalPlan(t *testing.T) {
	for _, plan := range []string{"free", "plus", "pro", "personal", "default"} {
		t.Run(plan, func(t *testing.T) {
			token := personalFixtureToken(time.Now(), func(c map[string]any) { c["https://api.openai.com/auth"].(map[string]any)["chatgpt_plan_type"] = plan })
			if _, err := postTOTPPersonalExpiry(token, time.Now()); err != nil {
				t.Fatalf("supported Personal plan %s rejected: %v", plan, err)
			}
		})
	}
}

func TestPersonalRefreshAndDiscoveryAcquireSeparateLeases(t *testing.T) {
	fixture := &personalFlowFixture{token: personalFixtureToken(time.Now(), nil)}
	client := &http.Client{Timeout: time.Second, Transport: accountsRoundTrip(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/backend-api/accounts/check/v4-2023-04-27" {
			if req.Method != "GET" || req.Header.Get("Authorization") != "Bearer "+fixture.token || !strings.Contains(req.Header.Get("Cookie"), "cookie-secret") {
				t.Fatalf("discovery escaped Personal session: %+v", req)
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"accounts":{"one":{"account":{"account_id":"team-1","name":"One","structure":"workspace","plan_type":"team"}}}}`)), Request: req}, nil
		}
		return fixture.RoundTrip(req)
	})}
	acquired, released := 0, 0
	factory := func(context.Context) (*http.Client, func(), error) {
		acquired++
		return client, func() { released++ }, nil
	}
	refreshed, err := (PersonalWebRefresher{Client: factory}).RefreshPersonal(t.Context(), MotherMaterial{LoginIdentifier: "mother@example.test", Password: "password", TOTPSecret: "JBSWY3DPEHPK3PXP"})
	if err != nil || refreshed.Status != "ready" {
		t.Fatalf("Personal refresh: %+v %v", refreshed, err)
	}
	discovered, err := (AccountsCheckDiscovery{Client: factory}).Discover(t.Context(), refreshed.Session)
	if err != nil || discovered.Status != "discovered" || len(discovered.Workspaces) != 1 || acquired != 2 || released != 2 {
		t.Fatalf("separate leases: %+v %v count %d/%d", discovered, err, acquired, released)
	}
}

func TestPersonalWebRefreshRejectsRedirectsClaimsAndBadTOTP(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*personalFlowFixture)
	}{
		{"external_signin", func(f *personalFlowFixture) { f.signin = "https://outside.test/steal" }},
		{"external_redirect", func(f *personalFlowFixture) { f.redirect = "https://outside.test/steal" }},
		{"external_mfa_continuation", func(f *personalFlowFixture) { f.mfaContinue = "https://outside.test/steal" }},
		{"workspace_selection_continuation", func(f *personalFlowFixture) { f.mfaContinue = "https://auth.openai.com/api/accounts/workspace/select" }},
		{"oauth_continuation", func(f *personalFlowFixture) { f.mfaContinue = "https://auth.openai.com/oauth/authorize" }},
		{"insecure_signin", func(f *personalFlowFixture) { f.signin = "http://auth.openai.com/login/start" }},
		{"invalid_password", func(f *personalFlowFixture) { f.passwordStatus = 401 }},
		{"invalid_totp", func(f *personalFlowFixture) { f.mfaStatus = 401 }},
		{"wrong_client_claim", func(f *personalFlowFixture) {
			f.token = personalFixtureToken(time.Now(), func(c map[string]any) {
				c["https://api.openai.com/auth"].(map[string]any)["client_id"] = "other-client"
			})
		}},
		{"missing_scope_claim", func(f *personalFlowFixture) {
			f.token = personalFixtureToken(time.Now(), func(c map[string]any) { c["https://api.openai.com/auth"].(map[string]any)["scp"] = []string{"openid"} })
		}},
		{"password_only_claim", func(f *personalFlowFixture) {
			f.token = personalFixtureToken(time.Now(), func(c map[string]any) { delete(c, "https://api.openai.com/mfa") })
		}},
		{"workspace_claim", func(f *personalFlowFixture) {
			f.token = personalFixtureToken(time.Now(), func(c map[string]any) { c["https://api.openai.com/auth"].(map[string]any)["chatgpt_plan_type"] = "k12" })
		}},
		{"forged_flat_workspace_claim", func(f *personalFlowFixture) {
			f.token = personalFixtureToken(time.Now(), func(c map[string]any) {
				c["https://api.openai.com/auth.chatgpt_plan_type"] = "k12"
				c["https://api.openai.com/auth.chatgpt_account_id"] = "workspace-1"
			})
		}},
		{"conflicting_account_id", func(f *personalFlowFixture) {
			f.token = personalFixtureToken(time.Now(), func(c map[string]any) { c["https://api.openai.com/auth.chatgpt_account_id"] = "other-personal" })
		}},
		{"conflicting_scopes", func(f *personalFlowFixture) {
			f.token = personalFixtureToken(time.Now(), func(c map[string]any) { c["https://api.openai.com/auth.scp"] = []string{"openid"} })
		}},
		{"conflicting_mfa", func(f *personalFlowFixture) {
			f.token = personalFixtureToken(time.Now(), func(c map[string]any) { c["https://api.openai.com/mfa.required"] = false })
		}},
		{"unknown_personal_plan", func(f *personalFlowFixture) {
			f.token = personalFixtureToken(time.Now(), func(c map[string]any) {
				c["https://api.openai.com/auth"].(map[string]any)["chatgpt_plan_type"] = "unclassified"
			})
		}},
		{"expired_claim", func(f *personalFlowFixture) {
			f.token = personalFixtureToken(time.Now(), func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() })
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := &personalFlowFixture{token: personalFixtureToken(time.Now(), nil)}
			tt.mutate(fixture)
			client := &http.Client{Transport: fixture, Timeout: time.Second}
			adapter := PersonalWebRefresher{Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}
			result, err := adapter.RefreshPersonal(t.Context(), MotherMaterial{LoginIdentifier: "mother@example.test", Password: "password", TOTPSecret: "JBSWY3DPEHPK3PXP"})
			if err != nil || result.Status == "ready" || result.Session.AccessToken != "" || fixture.blocked {
				t.Fatalf("unexpected result=%+v err=%v paths=%v", result, err, fixture.paths)
			}
		})
	}
	called := false
	adapter := PersonalWebRefresher{Client: func(context.Context) (*http.Client, func(), error) { called = true; return nil, nil, nil }}
	result, _ := adapter.RefreshPersonal(t.Context(), MotherMaterial{LoginIdentifier: "mother@example.test", Password: "password", TOTPSecret: "bad"})
	if called || result.Status != "refresh_failed" {
		t.Fatal("invalid TOTP reached transport")
	}
}
