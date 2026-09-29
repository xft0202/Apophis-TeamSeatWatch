package platform

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type accountsRoundTrip func(*http.Request) (*http.Response, error)

func (f accountsRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAccountsCheckDiscoveryUsesOnlySavedPersonalSessionAndReadOnlyEndpoint(t *testing.T) {
	calls, releases := 0, 0
	client := &http.Client{Transport: accountsRoundTrip(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodGet || req.URL.String() != accountsCheckURL || req.Header.Get("Authorization") != "Bearer personal-at" || req.Header.Get("oai-device-id") != "device-1" || !strings.Contains(req.Header.Get("Cookie"), "__Secure-next-auth.session-token=saved-cookie") {
			t.Fatalf("incorrect scoped GET: %v %v", req.URL, req.Header)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"accounts":{"personal":{"account":{"account_id":"personal","name":"Private","plan_type":"personal","structure":"personal"}},"one":{"account":{"account_id":"team-1","name":"First","plan_type":"team","structure":"workspace"}},"duplicate":{"account":{"account_id":"team-1","name":"Alias","plan_type":"team","structure":"workspace"}},"two":{"account":{"account_id":"team-2","name":"Second","plan_type":"business","structure":"workspace"}}}}`)), Request: req}, nil
	}), Timeout: time.Second}
	adapter := AccountsCheckDiscovery{Client: func(context.Context) (*http.Client, func(), error) { return client, func() { releases++ }, nil }}
	session := PersonalSession{AccessToken: "personal-at", DeviceID: "device-1", Cookies: []SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "saved-cookie"}}, ExpiresAt: time.Now().Add(time.Hour)}
	result, err := adapter.Discover(t.Context(), session)
	if err != nil || result.Status != "discovered" || len(result.Workspaces) != 2 || result.Workspaces[0].PlatformID != "team-1" || result.Workspaces[1].PlatformID != "team-2" || result.Workspaces[0].Access != "readable" || calls != 1 || releases != 1 {
		t.Fatalf("result=%+v err=%v calls=%d releases=%d", result, err, calls, releases)
	}
}

func TestAccountsCheckDiscoveryDoesNotTreatFailuresAsEmpty(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"empty", 200, `{"accounts":{}}`, "discovered"},
		{"missing_map", 200, `{"other":{}}`, "discovery_failed"},
		{"missing_account", 200, `{"accounts":{"team":{}}}`, "discovery_failed"},
		{"missing_structure", 200, `{"accounts":{"team":{"account":{"account_id":"team","name":"Team","plan_type":"team"}}}}`, "discovery_failed"},
		{"missing_plan", 200, `{"accounts":{"team":{"account":{"account_id":"team","name":"Team","structure":"workspace"}}}}`, "discovery_failed"},
		{"malformed", 200, `{"accounts":`, "discovery_failed"},
		{"unauthorized", 401, `{}`, "session_expired"},
		{"forbidden", 403, `{}`, "permission_denied"},
		{"other_4xx", 404, `{}`, "discovery_failed"},
		{"server", 503, `{}`, "discovery_failed"},
		{"redirect", 302, ``, "discovery_failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: accountsRoundTrip(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tt.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tt.body)), Request: req}, nil
			}), Timeout: time.Second}
			adapter := AccountsCheckDiscovery{Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}
			session := PersonalSession{AccessToken: "token", DeviceID: "device", Cookies: []SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "cookie"}}, ExpiresAt: time.Now().Add(time.Hour)}
			result, err := adapter.Discover(t.Context(), session)
			if err != nil || result.Status != tt.want || len(result.Workspaces) != 0 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
	client := &http.Client{Transport: accountsRoundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("transport failed") }), Timeout: time.Second}
	adapter := AccountsCheckDiscovery{Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}
	result, err := adapter.Discover(t.Context(), PersonalSession{AccessToken: "token", DeviceID: "device", Cookies: []SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "cookie"}}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil || result.Status != "discovery_failed" {
		t.Fatalf("transport result=%+v err=%v", result, err)
	}
}
