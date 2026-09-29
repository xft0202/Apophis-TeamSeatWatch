package platform

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type exchangeTransport func(*http.Request) (*http.Response, error)

func (fn exchangeTransport) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func exchangeFixture(t *testing.T, status int, account, token string) (OfficialWorkspaceTokenExchanger, *atomic.Int32) {
	t.Helper()
	calls := new(atomic.Int32)
	client := &http.Client{Timeout: time.Second, Transport: exchangeTransport(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if req.Method != http.MethodGet || req.URL.Scheme != "https" || req.URL.Host != "chatgpt.com" || req.URL.Path != "/api/auth/session" ||
			req.URL.RawQuery != "exchange_workspace_token=true&workspace_id=canonical-space&reason=setCurrentAccountWithoutRedirect" ||
			req.Header.Get("Authorization") != "" || req.Header.Get("Oai-Device-Id") != "saved-device" ||
			!strings.Contains(req.Header.Get("Cookie"), "_account=canonical-space") || !strings.Contains(req.Header.Get("Cookie"), "oai-workspace=canonical-space") ||
			!strings.Contains(req.Header.Get("Cookie"), "__Secure-next-auth.session-token=saved-cookie") {
			t.Errorf("exchange crossed allowlist/session boundary: %s %s %v", req.Method, req.URL, req.Header)
		}
		payload, _ := json.Marshal(map[string]any{"accessToken": token, "account": map[string]any{"id": account}, "expires": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
		header := http.Header{}
		if status == http.StatusOK {
			header.Add("Set-Cookie", "__Secure-next-auth.session-token=workspace-cookie; Path=/; Secure")
		}
		if status == http.StatusFound {
			header.Add("Location", "https://attacker.example/")
		}
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(string(payload))), Request: req}, nil
	})}
	return OfficialWorkspaceTokenExchanger{Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}, calls
}

func TestOfficialWorkspaceExchangeIsExplicitIsolatedAndIdentityFenced(t *testing.T) {
	personal := PersonalSession{AccessToken: "personal-token", DeviceID: "saved-device", Cookies: []SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "saved-cookie"}, {Name: "_account", Value: "personal-account"}}, ExpiresAt: time.Now().Add(3 * time.Hour)}
	for _, scenario := range []struct {
		name           string
		status         int
		account, token string
		success        bool
	}{
		{"match", 200, "canonical-space", workspaceFixtureToken("canonical-space"), true},
		{"wrong_session_account", 200, "different-space", workspaceFixtureToken("canonical-space"), false},
		{"wrong_jwt_workspace", 200, "canonical-space", workspaceFixtureToken("different-space"), false},
		{"redirect", 302, "canonical-space", workspaceFixtureToken("canonical-space"), false},
		{"forbidden", 403, "canonical-space", workspaceFixtureToken("canonical-space"), false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			exchanger, calls := exchangeFixture(t, scenario.status, scenario.account, scenario.token)
			access, err := exchanger.ExchangeWorkspace(context.Background(), personal, "canonical-space")
			if calls.Load() != 1 {
				t.Fatalf("exchange request count=%d", calls.Load())
			}
			if (err == nil) != scenario.success {
				t.Fatalf("exchange success=%v want=%v", err == nil, scenario.success)
			}
			if scenario.success && (!ValidateWorkspaceAccess(access, "canonical-space", time.Now()) || access.Cookies[0].Value == "saved-cookie") {
				t.Fatalf("Workspace token/cookies not isolated: %+v", access)
			}
			if !scenario.success && access.AccessToken != "" {
				t.Fatal("invalid exchange retained bearer")
			}
		})
	}
	if personal.Cookies[0].Value != "saved-cookie" || personal.Cookies[1].Value != "personal-account" {
		t.Fatal("saved Personal cookies mutated")
	}
}

func TestWorkspaceTokenJWTExpiryAndConflictingClaimsFailClosed(t *testing.T) {
	access := selectedSession()
	if !ValidateWorkspaceAccess(access, "canonical-space", time.Now()) {
		t.Fatal("valid fixture rejected")
	}
	access.AccessToken = workspaceFixtureToken("other-space")
	if ValidateWorkspaceAccess(access, "canonical-space", time.Now()) {
		t.Fatal("mismatched JWT accepted")
	}
	access = selectedSession()
	access.ExpiresAt = time.Now().Add(-time.Second)
	if ValidateWorkspaceAccess(access, "canonical-space", time.Now()) {
		t.Fatal("expired bearer accepted")
	}
}
