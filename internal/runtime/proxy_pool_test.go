package runtime

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/egress"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

func TestBrowserVerificationFailuresKeepProxyForRetry(t *testing.T) {
	for _, code := range []string{"proxy_browser_challenge", "proxy_browser_unavailable", "proxy_browser_verification_incomplete"} {
		if permanentlyUnusableProxy(code, nil) {
			t.Fatalf("browser verification failure removes a potentially usable proxy: %s", code)
		}
	}
}

func TestProxyMutationsRequireOwnerSession(t *testing.T) {
	origins, err := auth.ParseOriginPolicy("https://owner.test")
	if err != nil {
		t.Fatal(err)
	}
	handler := ownerapi.Handler(&OwnerAuthHandler{origins: origins})
	csrf := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	for _, action := range []struct{ method, path string }{
		{http.MethodPut, "/source"},
		{http.MethodPost, "/source/test"},
		{http.MethodPost, "/sync"},
		{http.MethodPost, "/nodes/import"},
		{http.MethodPost, "/probe"},
		{http.MethodPost, "/nodes/00000000-0000-4000-8000-000000000001/probe"},
		{http.MethodDelete, "/nodes/00000000-0000-4000-8000-000000000001"},
		{http.MethodPatch, "/settings"},
	} {
		t.Run(action.method+action.path, func(t *testing.T) {
			request := httptest.NewRequest(action.method, "/api/owner/v1/proxy-pool"+action.path, strings.NewReader(`{}`))
			request.Header.Set("Origin", "https://owner.test")
			request.Header.Set(auth.CSRFHeaderName, csrf)
			request.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated proxy mutation status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestProxySubscriptionFormats(t *testing.T) {
	want := []string{"http://user:pass@proxy.example:8080", "socks5h://proxy.example:1080"}
	plain := strings.Join(want, "\n")
	inputs := []string{plain + "\n" + want[0] + "\nftp://ignored.example:21", strings.Join(want, ",")}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		inputs = append(inputs, encoding.EncodeToString([]byte(plain)))
	}
	for _, input := range inputs {
		if got := parseSubscription([]byte(input)); !reflect.DeepEqual(got, want) {
			t.Fatalf("parsed subscription=%v want=%v", got, want)
		}
	}
}

func TestManagedProxyAcceptsProviderCredentialWithSpace(t *testing.T) {
	for _, raw := range []string{
		"socks5://provider-region-US-st-North Carolina-sid-session-t-120:password@proxy.example.test:443",
		"socks5://provider-region-US-st-North Carolina-city-Charlotte-sid-session-t-120:password@proxy.example.test:443",
	} {
		scheme, host, err := parseManagedProxy(raw)
		if err != nil {
			t.Fatalf("provider proxy rejected: %v", err)
		}
		if scheme != "socks5" || host != "proxy.example.test:443" {
			t.Fatalf("parsed proxy=%s %s", scheme, host)
		}
		got := parseSubscription([]byte(raw))
		want := egress.NormalizeEndpointURL(raw)
		if !reflect.DeepEqual(got, []string{want}) {
			t.Fatalf("normalized provider proxy=%v want=%v", got, []string{want})
		}
	}
}

func TestProxySubscriptionRejectsEndpointURLs(t *testing.T) {
	for _, raw := range []string{"", "socks5://proxy.example:1080", "ftp://proxy.example:21", "https://user:pass@proxy.example/sub", "https://proxy.example/sub#secret"} {
		if err := validateSubscriptionURL(raw); err == nil {
			t.Fatalf("invalid subscription URL accepted")
		}
	}
}

func TestProxySubscriptionFetchPreservesQueryAndRejectsOversizedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "test-secret" {
			t.Error("subscription query was lost")
		}
		if r.URL.Path == "/large" {
			_, _ = w.Write([]byte(strings.Repeat("x", proxyFetchLimit+1)))
			return
		}
		_, _ = w.Write([]byte("http://proxy.example:8080"))
	}))
	defer server.Close()
	if _, err := fetchSubscription(context.Background(), server.URL+"/sub?token=test-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := fetchSubscription(context.Background(), server.URL+"/large?token=test-secret"); err == nil {
		t.Fatal("oversized subscription was accepted")
	}
}
