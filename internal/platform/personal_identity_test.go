package platform

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"
)

func identitySession(t *testing.T, change func(map[string]any)) PersonalSession {
	t.Helper()
	s := rotationJoinFixtureSession(time.Now())
	parts := strings.Split(s.AccessToken, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	claims["email"] = "candidate@example.test"
	claims["sub"] = "auth0|candidate"
	claims["https://api.openai.com/auth"].(map[string]any)["chatgpt_user_id"] = "candidate-user"
	if change != nil {
		change(claims)
	}
	raw, _ = json.Marshal(claims)
	s.AccessToken = parts[0] + "." + base64.RawURLEncoding.EncodeToString(raw) + "." + parts[2]
	return s
}
func identityBody(s PersonalSession) map[string]any {
	return map[string]any{"accessToken": s.AccessToken, "expires": s.ExpiresAt.Format(time.RFC3339Nano), "user": map[string]any{"id": "auth0|candidate", "email": "candidate@example.test"}}
}
func TestPersonalIdentityExactAuthenticatedToken(t *testing.T) {
	for _, tc := range []struct {
		name     string
		claims   func(map[string]any)
		snapshot func(map[string]any)
		valid    bool
	}{
		{name: "distinct_user_and_sub", valid: true},
		{name: "sub_fallback", claims: func(c map[string]any) { delete(c["https://api.openai.com/auth"].(map[string]any), "chatgpt_user_id") }, valid: true},
		{name: "missing_subject", claims: func(c map[string]any) {
			delete(c, "sub")
			delete(c["https://api.openai.com/auth"].(map[string]any), "chatgpt_user_id")
		}},
		{name: "account_is_not_subject", claims: func(c map[string]any) {
			delete(c, "sub")
			delete(c["https://api.openai.com/auth"].(map[string]any), "chatgpt_user_id")
		}, snapshot: func(b map[string]any) { b["user"].(map[string]any)["id"] = "" }},
		{name: "conflicting_subject", claims: func(c map[string]any) { c["chatgpt_user_id"] = "another-user" }},
		{name: "conflicting_sub", claims: func(c map[string]any) { c["https://api.openai.com/auth.sub"] = "another-sub" }},
		{name: "conflicting_profile", claims: func(c map[string]any) {
			c["https://api.openai.com/profile"] = map[string]any{"email": "wrong@example.test"}
		}},
		{name: "conflicting_response", snapshot: func(b map[string]any) { b["user"].(map[string]any)["email"] = "wrong@example.test" }},
		{name: "wrong_cookie_account", snapshot: func(b map[string]any) { b["accessToken"] = "another-token" }},
		{name: "wrong_response_user", snapshot: func(b map[string]any) { b["user"].(map[string]any)["id"] = "another-user" }},
		{name: "missing_identifier", claims: func(c map[string]any) { delete(c, "email") }, snapshot: func(b map[string]any) { b["user"].(map[string]any)["email"] = "" }},
		{name: "malformed_identifier", claims: func(c map[string]any) { c["email"] = []string{"candidate@example.test"} }},
		{name: "workspace_scope", claims: func(c map[string]any) {
			c["https://api.openai.com/auth"].(map[string]any)["chatgpt_plan_type"] = "team"
		}},
		{name: "stale_session", snapshot: func(b map[string]any) { b["expires"] = time.Now().Add(-time.Minute).Format(time.RFC3339Nano) }},
		{name: "canonical_identifier", claims: func(c map[string]any) { c["email"] = " Candidate@Example.Test " }, valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := identitySession(t, tc.claims)
			snapshot := identityBody(s)
			if tc.snapshot != nil {
				tc.snapshot(snapshot)
			}
			raw, _ := json.Marshal(snapshot)
			calls, released := 0, 0
			client := &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "GET" || r.URL.String() != "https://chatgpt.com/api/auth/session" || r.Body != nil {
					t.Fatal("invented identity route/action")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			})}
			c := OfficialPersonalIdentityConfirmer{Client: func(context.Context) (*http.Client, func(), error) { return client, func() { released++ }, nil }}
			got, err := c.ConfirmPersonalIdentity(context.Background(), s)
			if tc.valid {
				if err != nil || got.Identifier != "candidate@example.test" || got.SubjectID == "" || got.ObservedAt.IsZero() || calls != 1 || released != 1 {
					t.Fatalf("identity=%+v err=%v calls=%d release=%d", got, err, calls, released)
				}
			} else if !errors.Is(err, ErrPersonalIdentityUnavailable) || got != (PersonalIdentity{}) {
				t.Fatalf("untrusted identity accepted: %+v %v", got, err)
			}
		})
	}
}
func TestPersonalIdentityTransportIsolationAndRedaction(t *testing.T) {
	s := identitySession(t, nil)
	jar, _ := cookiejar.New(nil)
	origin, _ := url.Parse("https://chatgpt.com/")
	jar.SetCookies(origin, []*http.Cookie{{Name: "ambient-secret", Value: "ambient-cookie"}})
	for _, mode := range []string{"redirect", "transport", "oversize", "malformed", "cancel", "nil_transport"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			client := &http.Client{Jar: jar, Timeout: time.Second, Transport: rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if _, err := r.Cookie("ambient-secret"); err == nil {
					t.Fatal("ambient cookie leaked")
				}
				if cookie, err := r.Cookie("__Secure-next-auth.session-token"); err != nil || cookie.Value != s.Cookies[0].Value {
					t.Fatal("saved session cookie absent")
				}
				if mode == "transport" {
					return nil, errors.New(s.AccessToken + " cookie-secret")
				}
				body := "invalid-secret-body"
				status := 200
				header := http.Header{}
				if mode == "redirect" {
					status = 302
					header.Set("Location", "https://evil.example/")
				}
				if mode == "oversize" {
					body = strings.Repeat("x", 65537)
				}
				return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			if mode == "nil_transport" {
				client.Transport = nil
			}
			c := OfficialPersonalIdentityConfirmer{Client: func(context.Context) (*http.Client, func(), error) { return client, nil, nil }}
			got, err := c.ConfirmPersonalIdentity(ctx, s)
			if err != ErrPersonalIdentityUnavailable || got != (PersonalIdentity{}) || strings.Contains(err.Error(), s.AccessToken) || calls > 1 {
				t.Fatalf("unsafe result=%+v err=%v calls=%d", got, err, calls)
			}
		})
	}
}

func TestPersonalIdentityAfterAuthenticatedUsage(t *testing.T) {
	s := identitySession(t, nil)
	identity, err := IdentityAfterPersonalProbe(s, PersonalProbeEvidence{HTTPStatus: 200, VerifiedUsage: true})
	if err != nil || identity.SubjectID != "candidate-user" || identity.Identifier != "candidate@example.test" {
		t.Fatalf("identity=%+v error=%v", identity, err)
	}
	for _, evidence := range []PersonalProbeEvidence{{HTTPStatus: 403}, {HTTPStatus: 200}, {HTTPStatus: 200, VerifiedUsage: true, Malformed: true}} {
		if _, err := IdentityAfterPersonalProbe(s, evidence); err == nil {
			t.Fatal("unauthenticated response admitted an identity")
		}
	}
}
