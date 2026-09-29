package platform

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type usageTransport func(*http.Request) (*http.Response, error)

func (f usageTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestPersonalUsageProbeFixedBearerGETAndClassification(t *testing.T) {
	session := PersonalSession{AccessToken: "personal-at", DeviceID: "device", Cookies: []SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "cookie"}}, ExpiresAt: time.Now().Add(time.Hour)}
	cases := []struct {
		name              string
		status            int
		contentType, body string
		want              PersonalProbeOutcome
	}{
		{"available", 200, "application/json", `{"rate_limit":{"primary_window":{"used_percent":20}}}`, PersonalAvailable},
		{"401", 401, "text/plain", "expired", PersonalCredentialInvalid},
		{"forbidden", 403, "application/json", `{"error":{"code":"not_allowed"}}`, PersonalForbidden},
		{"deactivated", 403, "application/json", `{"error":{"code":"account_deactivated"}}`, PersonalBanned},
		{"unstructured", 403, "text/html", `account_deactivated`, PersonalForbidden},
		{"malformed", 200, "application/json", `{}`, PersonalUnknown},
		{"unknown usage shape", 200, "application/json", `{"rate_limit":{"unexpected":"value"}}`, PersonalUnknown},
		{"redirect", 302, "text/html", `<html>redirect</html>`, PersonalUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			released, calls := false, 0
			p := PersonalUsageProbe{Client: func(context.Context) (*http.Client, func(), error) {
				return &http.Client{Timeout: time.Second, Transport: usageTransport(func(req *http.Request) (*http.Response, error) {
					calls++
					if req.Method != http.MethodGet || req.URL.String() != personalUsageURL || req.Header.Get("Authorization") != "Bearer personal-at" || req.Header.Get("Proxy-Authorization") != "" || req.Header.Get("chatgpt-account-id") != "" {
						t.Fatalf("unsafe usage request: %s %s %+v", req.Method, req.URL, req.Header)
					}
					return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": []string{tc.contentType}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
				})}, func() { released = true }, nil
			}}
			evidence, err := p.Probe(t.Context(), session)
			if err != nil || ClassifyPersonalProbe(evidence) != tc.want || calls != 1 || !released {
				t.Fatalf("evidence %+v err %v calls %d released %t", evidence, err, calls, released)
			}
		})
	}
}

func TestPersonalUsageProbeRejectsOversizeAndMissingSession(t *testing.T) {
	calls := 0
	p := PersonalUsageProbe{Client: func(context.Context) (*http.Client, func(), error) {
		return &http.Client{Timeout: time.Second, Transport: usageTransport(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 403, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"account_deactivated"},"padding":"` + strings.Repeat("x", maxPersonalUsageBytes) + `"}`))}, nil
		})}, func() {}, nil
	}}
	if _, err := p.Probe(t.Context(), PersonalSession{}); err == nil || calls != 0 {
		t.Fatal("missing session reached transport")
	}
	valid := PersonalSession{AccessToken: "at", DeviceID: "device", Cookies: []SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "cookie"}}, ExpiresAt: time.Now().Add(time.Hour)}
	evidence, err := p.Probe(t.Context(), valid)
	if err != nil || ClassifyPersonalProbe(evidence) == PersonalBanned {
		t.Fatalf("oversized 403 certified ban: %+v %v", evidence, err)
	}
}
