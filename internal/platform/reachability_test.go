package platform

import (
	"errors"
	"net/http"
	"testing"

	"github.com/chromedp/cdproto/network"
)

func TestReachabilityPreservesHTTPRejectionWhenDocumentIsIncomplete(t *testing.T) {
	for _, status := range []int{401, 403, 407, 408, 429, 500, 503} {
		response := &http.Response{StatusCode: status, Header: make(http.Header), Body: http.NoBody}
		result := reachabilityOutcome(response, errors.New("document not ready"))
		if result.HTTPStatus != status || result.Code != "" {
			t.Fatalf("received HTTP rejection lost to document readiness: status=%d result=%+v", status, result)
		}
	}
	response := &http.Response{StatusCode: 403, Header: http.Header{"Cf-Mitigated": []string{"challenge"}}, Body: http.NoBody}
	if result := reachabilityOutcome(response, errors.New("document not ready")); result.Code != "proxy_browser_challenge" || result.HTTPStatus != 403 {
		t.Fatalf("browser challenge classification lost: %+v", result)
	}
	response = &http.Response{StatusCode: 200, Header: make(http.Header), Body: http.NoBody}
	if result := reachabilityOutcome(response, errors.New("document not ready")); result.Code != "proxy_browser_verification_incomplete" {
		t.Fatal("incomplete successful document was admitted")
	}
}

func TestReachabilityBrowserCannotSubmitAccountOrWorkspaceActions(t *testing.T) {
	for _, input := range []struct{ method, target string }{
		{"POST", "https://auth.openai.com/api/accounts/password/verify"},
		{"POST", "https://chatgpt.com/backend-api/accounts/workspace/invites"},
		{"GET", "https://auth.openai.com/oauth/authorize"},
		{"GET", "https://chatgpt.com/backend-api/accounts/check/v4-2023-04-27"},
		{"GET", "https://example.com/"},
		{"GET", "https://chatgpt.com:8443/"},
		{"GET", "https://secret@chatgpt.com/"},
	} {
		if allowedReachabilityBrowserRequest(&network.Request{Method: input.method, URL: input.target}) {
			t.Fatalf("anonymous reachability browser escaped its scope: %s %s", input.method, input.target)
		}
	}
	for _, input := range []struct{ method, target string }{
		{"GET", "https://chatgpt.com/"},
		{"GET", "https://cdn.oaistatic.com/assets/main.js"},
		{"POST", "https://chatgpt.com/cdn-cgi/challenge-platform/verify"},
		{"POST", "https://challenges.cloudflare.com/verify"},
	} {
		if !allowedReachabilityBrowserRequest(&network.Request{Method: input.method, URL: input.target}) {
			t.Fatalf("first-party reachability verification rejected: %s %s", input.method, input.target)
		}
	}
}

func TestReachabilityContinuationRemainsOnAnonymousHomepage(t *testing.T) {
	for _, target := range []string{"https://chatgpt.com/", "https://chatgpt.com"} {
		request, _ := http.NewRequest(http.MethodGet, target, nil)
		if err := validateReachabilityRequest(request); err != nil {
			t.Fatal(err)
		}
	}
	for _, target := range []string{"https://chatgpt.com/auth/login", "https://auth.openai.com/oauth/authorize", "http://chatgpt.com/", "https://user:password@chatgpt.com/"} {
		request, _ := http.NewRequest(http.MethodGet, target, nil)
		if validateReachabilityRequest(request) == nil {
			t.Fatalf("reachability continuation escaped its anonymous document: %s", target)
		}
	}
}
