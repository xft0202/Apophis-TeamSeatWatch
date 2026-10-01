package platform

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestRotationJoinUsesOnlyPersonalBearerAndFrozenWorkspace verifies the adapter
// authenticates with Personal token + deviceID only, and never uses BasicAuth,
// password gateways, or ambient cookies.
func TestRotationJoinUsesOnlyPersonalBearerAndFrozenWorkspace(t *testing.T) {
	session := rotationJoinFixtureSession(time.Now())
	calls := 0
	client := &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		expectedPath := "/backend-api/accounts/workspace-1/invites/request"
		if r.URL.String() != "https://chatgpt.com"+expectedPath {
			t.Fatalf("unexpected URL: %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer "+session.AccessToken {
			t.Fatalf("Personal bearer missing or wrong: %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("oai-device-id") != "fixture-device" {
			t.Fatal("deviceID missing")
		}
		if r.Header.Get("X-Openai-Target-Path") != expectedPath || r.Header.Get("X-Openai-Target-Route") != "/backend-api/accounts/{account_id}/invites/request" {
			t.Fatal("semantic route headers missing")
		}
		if _, _, basic := r.BasicAuth(); basic {
			t.Fatal("BasicAuth used when Personal bearer was supplied")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "{}" {
			t.Fatalf("unexpected body: %s", body)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"status":"ok"}`))}, nil
	})}
	j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}
	result, err := j.RequestCandidateJoin(context.Background(), session, "workspace-1")
	if err != nil || !result.RequestSent || calls != 1 {
		t.Fatalf("receipt=%+v err=%v calls=%d", result, err, calls)
	}
}

// TestRotationJoinSplitStagesNeverCombineRequestAndAccept verifies the adapter
// exposes two separate methods so the caller can fence and persist a durable
// stage marker between them.
func TestRotationJoinSplitStagesNeverCombineRequestAndAccept(t *testing.T) {
	session := rotationJoinFixtureSession(time.Now())
	calls := 0
	paths := []string{}
	client := &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		paths = append(paths, r.URL.Path)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}
	requestResult, err := j.RequestCandidateJoin(context.Background(), session, "workspace-1")
	if err != nil || !requestResult.RequestSent || calls != 1 {
		t.Fatalf("request receipt=%+v err=%v calls=%d", requestResult, err, calls)
	}
	acceptResult, err := j.AcceptCandidateJoin(context.Background(), session, "workspace-1")
	if err != nil || !acceptResult.RequestSent || calls != 2 {
		t.Fatalf("accept receipt=%+v err=%v calls=%d", acceptResult, err, calls)
	}
	if len(paths) != 2 || paths[0] != "/backend-api/accounts/workspace-1/invites/request" || paths[1] != "/backend-api/accounts/workspace-1/invites/accept" {
		t.Fatalf("expected split stages; got paths=%v", paths)
	}
}

// TestRotationJoinAcceptSetsExplicitAccountCookie verifies the accept stage
// writes the _accept_account_id cookie seeded with the frozen workspace, and the
// request stage never carries that cookie.
func TestRotationJoinAcceptSetsExplicitAccountCookie(t *testing.T) {
	session := rotationJoinFixtureSession(time.Now())
	seenCookies := make(map[string]string)
	client := &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
		path := r.URL.Path
		for _, cookie := range r.Cookies() {
			seenCookies[path+":"+cookie.Name] = cookie.Value
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}
	_, _ = j.RequestCandidateJoin(context.Background(), session, "workspace-1")
	_, _ = j.AcceptCandidateJoin(context.Background(), session, "workspace-1")
	requestAcceptCookie := seenCookies["/backend-api/accounts/workspace-1/invites/request:_accept_account_id"]
	acceptAcceptCookie := seenCookies["/backend-api/accounts/workspace-1/invites/accept:_accept_account_id"]
	if requestAcceptCookie != "" {
		t.Fatal("request stage carried _accept_account_id cookie")
	}
	if acceptAcceptCookie != "workspace-1" {
		t.Fatalf("accept stage _accept_account_id=%s; want workspace-1", acceptAcceptCookie)
	}
}

// TestRotationJoinIsolatedJarNeverMixesAmbientCookies verifies the adapter
// creates a fresh jar seeded only from the supplied Personal cookies, and never
// inherits ambient cookies from the leased client.
func TestRotationJoinIsolatedJarNeverMixesAmbientCookies(t *testing.T) {
	session := rotationJoinFixtureSession(time.Now())
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("https://chatgpt.com")
	jar.SetCookies(origin, []*http.Cookie{{Name: "unrelated-ambient", Value: "should-not-appear"}})
	seenUnrelated := false
	seenSession := false
	client := &http.Client{Timeout: time.Second, Jar: jar, Transport: rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
		for _, cookie := range r.Cookies() {
			if cookie.Name == "unrelated-ambient" {
				seenUnrelated = true
			}
			if cookie.Name == "__Secure-next-auth.session-token" && cookie.Value == "fixture-session-cookie" {
				seenSession = true
			}
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}
	_, err = j.RequestCandidateJoin(context.Background(), session, "workspace-1")
	if err != nil {
		t.Fatal(err)
	}
	if seenUnrelated {
		t.Fatal("ambient cookie from leased client jar was carried")
	}
	if !seenSession {
		t.Fatal("supplied Personal session cookie was not carried")
	}
}

// TestRotationJoinRejectsWorkspaceBearer verifies the adapter rejects a
// Workspace-scoped JWT in place of a Personal token, so a Workspace bearer can
// never authenticate this seam.
func TestRotationJoinRejectsWorkspaceBearer(t *testing.T) {
	now := time.Now()
	workspaceToken := workspaceFixtureTokenWithClaims(map[string]any{
		"exp": now.Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "workspace-1",
			"chatgpt_plan_type":  "k12",
			"scp":                []string{"organization.read"},
		},
	})
	session := PersonalSession{
		AccessToken: workspaceToken,
		DeviceID:    "fixture-device",
		Cookies:     []SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "fixture"}},
		ExpiresAt:   now.Add(time.Hour),
	}
	calls := 0
	client := &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}
	result, err := j.RequestCandidateJoin(context.Background(), session, "workspace-1")
	if err != ErrRotationJoinUnavailable || result.RequestSent || calls != 0 {
		t.Fatalf("Workspace bearer accepted: result=%+v err=%v calls=%d", result, err, calls)
	}
}

// TestRotationJoinRejectsExpiredPersonalSession verifies predispatch validation
// rejects an expired Personal token before any request is sent.
func TestRotationJoinRejectsExpiredPersonalSession(t *testing.T) {
	now := time.Now()
	session := rotationJoinFixtureSession(now)
	session.ExpiresAt = now.Add(-time.Minute)
	calls := 0
	client := &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}
	result, err := j.RequestCandidateJoin(context.Background(), session, "workspace-1")
	if err != ErrRotationJoinUnavailable || result.RequestSent || calls != 0 {
		t.Fatalf("expired session accepted: result=%+v err=%v calls=%d", result, err, calls)
	}
}

// TestRotationJoinRejectsInvalidWorkspaceSegment verifies the adapter rejects
// empty, path-traversal, whitespace and unsafe workspace segments before any
// request is built.
func TestRotationJoinRejectsInvalidWorkspaceSegment(t *testing.T) {
	session := rotationJoinFixtureSession(time.Now())
	cases := []struct {
		name        string
		workspaceID string
	}{
		{"empty", ""},
		{"whitespace", " workspace-1 "},
		{"traversal", "../workspace-1"},
		{"slash", "workspace/1"},
		{"backslash", "workspace\\1"},
		{"query", "workspace?id=1"},
		{"fragment", "workspace#1"},
		{"percent", "workspace%201"},
		{"dot", "."},
		{"dotdot", ".."},
		{"newline", "workspace\n1"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
			})}
			j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}
			result, err := j.RequestCandidateJoin(context.Background(), session, tt.workspaceID)
			if err != ErrRotationJoinUnavailable || result.RequestSent || calls != 0 {
				t.Fatalf("invalid workspace accepted: result=%+v err=%v calls=%d", result, err, calls)
			}
		})
	}
}

// TestRotationJoinCancellationBeforeClientAcquisition verifies a cancelled
// context is detected before the egress lease is acquired.
func TestRotationJoinCancellationBeforeClientAcquisition(t *testing.T) {
	session := rotationJoinFixtureSession(time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	client := &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}
	result, err := j.RequestCandidateJoin(ctx, session, "workspace-1")
	if err != ErrRotationJoinUnavailable || result.RequestSent || calls != 0 {
		t.Fatalf("cancelled context accepted: result=%+v err=%v calls=%d", result, err, calls)
	}
}

// TestRotationJoinNeverFollowsRedirects verifies the adapter refuses to follow
// HTTP redirects, so a redirect endpoint cannot turn this adapter into an
// arbitrary-host request.
func TestRotationJoinNeverFollowsRedirects(t *testing.T) {
	session := rotationJoinFixtureSession(time.Now())
	calls := 0
	client := &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls > 1 {
			t.Fatal("redirect was followed")
		}
		header := http.Header{}
		header.Set("Location", "https://evil.example/captured")
		return &http.Response{StatusCode: http.StatusFound, Header: header, Body: io.NopCloser(strings.NewReader(``))}, nil
	})}
	j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}
	result, err := j.RequestCandidateJoin(context.Background(), session, "workspace-1")
	if err != nil || result.HTTPStatus != http.StatusFound || calls != 1 {
		t.Fatalf("redirect followed or not detected: result=%+v err=%v calls=%d", result, err, calls)
	}
}

// TestRotationJoinTransportErrorRemainsUncertain verifies a transport timeout,
// read error, or context cancellation after the request was sent reports
// RequestSent=true and RequestMayHaveEffect=true, because the POST may have been
// delivered upstream even though the local call failed.
func TestRotationJoinTransportErrorRemainsUncertain(t *testing.T) {
	session := rotationJoinFixtureSession(time.Now())
	client := &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})}
	j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}
	result, err := j.RequestCandidateJoin(context.Background(), session, "workspace-1")
	if err != ErrRotationJoinUnavailable || !result.RequestSent || !result.RequestMayHaveEffect {
		t.Fatalf("transport error treated as provably unsent: result=%+v err=%v", result, err)
	}
}

// TestRotationJoinMalformedResponseBodyRemainsUncertain verifies an incomplete,
// oversized, or malformed body after the POST was sent reports
// RequestMayHaveEffect=true, because the upstream receipt cannot be classified.
func TestRotationJoinMalformedResponseBodyRemainsUncertain(t *testing.T) {
	session := rotationJoinFixtureSession(time.Now())
	client := &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(errReader{})}, nil
	})}
	j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}
	result, err := j.RequestCandidateJoin(context.Background(), session, "workspace-1")
	if err != ErrRotationJoinUnavailable || !result.RequestSent || !result.RequestMayHaveEffect {
		t.Fatalf("malformed body treated as classified: result=%+v err=%v", result, err)
	}
}

// TestRotationJoinReceiptNeverVerifiesMembership verifies a 2xx receipt from
// either stage is never treated as verified membership or a usable deliverable.
func TestRotationJoinReceiptNeverVerifiesMembership(t *testing.T) {
	session := rotationJoinFixtureSession(time.Now())
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"ok_empty", http.StatusOK, ""},
		{"ok_success_true", http.StatusOK, `{"success":true}`},
		{"accepted", http.StatusAccepted, `{"status":"pending"}`},
		{"no_content", http.StatusNoContent, ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tt.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tt.body))}, nil
			})}
			j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}
			result, err := j.RequestCandidateJoin(context.Background(), session, "workspace-1")
			if err != nil || result.HTTPStatus != tt.status {
				t.Fatalf("receipt rejected: result=%+v err=%v", result, err)
			}
			// Success=true records HTTP 2xx only, never verified membership.
			// The caller must still check actual membership.
			if !result.Success {
				t.Fatal("2xx not classified as Success")
			}
		})
	}
}

// TestRotationJoinClientReleaseCalledOnce verifies the leased client release
// function is called exactly once even when client acquisition or transport fails.
func TestRotationJoinClientReleaseCalledOnce(t *testing.T) {
	session := rotationJoinFixtureSession(time.Now())
	released := 0
	client := &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) {
		return client, func() { released++ }, nil
	}}
	_, _ = j.RequestCandidateJoin(context.Background(), session, "workspace-1")
	if released != 1 {
		t.Fatalf("release called %d times; want 1", released)
	}
}

// TestRotationJoinStaleWorkspaceCookieRemoved verifies a supplied _account or
// oai-workspace cookie that disagrees with the frozen workspace is removed, so a
// stale selection hint cannot redirect the POST to another account.
func TestRotationJoinStaleWorkspaceCookieRemoved(t *testing.T) {
	now := time.Now()
	session := rotationJoinFixtureSession(now)
	session.Cookies = append(session.Cookies, SessionCookie{Name: "_account", Value: "workspace-stale"})
	seenStale := false
	client := &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
		for _, cookie := range r.Cookies() {
			if cookie.Name == "_account" && cookie.Value == "workspace-stale" {
				seenStale = true
			}
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}
	_, err := j.RequestCandidateJoin(context.Background(), session, "workspace-1")
	if err != nil {
		t.Fatal(err)
	}
	if seenStale {
		t.Fatal("stale _account cookie was carried and could redirect the POST")
	}
}

// TestRotationJoinMatchingWorkspaceCookieCarried verifies a supplied _account
// cookie that already matches the frozen workspace is carried unchanged.
func TestRotationJoinMatchingWorkspaceCookieCarried(t *testing.T) {
	now := time.Now()
	session := rotationJoinFixtureSession(now)
	session.Cookies = append(session.Cookies, SessionCookie{Name: "_account", Value: "workspace-1"})
	seenMatching := false
	client := &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
		for _, cookie := range r.Cookies() {
			if cookie.Name == "_account" && cookie.Value == "workspace-1" {
				seenMatching = true
			}
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}
	_, err := j.RequestCandidateJoin(context.Background(), session, "workspace-1")
	if err != nil {
		t.Fatal(err)
	}
	if !seenMatching {
		t.Fatal("matching _account cookie was removed")
	}
}

// TestRotationJoinTransportFenceRejectsArbitraryTargets verifies the transport
// guard rejects any continuation that changes host, scheme, port, path, query or
// method, so a shared helper cannot widen this seam into an arbitrary request.
func TestRotationJoinTransportFenceRejectsArbitraryTargets(t *testing.T) {
	next := rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
		t.Fatal("guard allowed a target it should have rejected")
		return nil, nil
	})
	guard := rotationJoinGuard{next: next, path: "/backend-api/accounts/workspace-1/invites/request", method: http.MethodPost}
	cases := []struct {
		name   string
		method string
		url    string
	}{
		{"other_host", http.MethodPost, "https://evil.example/backend-api/accounts/workspace-1/invites/request"},
		{"http_scheme", http.MethodPost, "http://chatgpt.com/backend-api/accounts/workspace-1/invites/request"},
		{"explicit_port", http.MethodPost, "https://chatgpt.com:443/backend-api/accounts/workspace-1/invites/request"},
		{"other_path", http.MethodPost, "https://chatgpt.com/backend-api/accounts/workspace-2/invites/request"},
		{"query_added", http.MethodPost, "https://chatgpt.com/backend-api/accounts/workspace-1/invites/request?x=1"},
		{"wrong_method", http.MethodGet, "https://chatgpt.com/backend-api/accounts/workspace-1/invites/request"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, tt.url, strings.NewReader("{}"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := guard.RoundTrip(req); err != ErrRotationJoinUnavailable {
				t.Fatalf("guard admitted %s %s: %v", tt.method, tt.url, err)
			}
		})
	}
}

// TestRotationJoinRequestStageNeverAccepts verifies the request stage performs
// exactly one POST to the request route and never chains an accept, retry, or
// follow-up call automatically.
func TestRotationJoinRequestStageNeverAccepts(t *testing.T) {
	session := rotationJoinFixtureSession(time.Now())
	calls := 0
	client := &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != "/backend-api/accounts/workspace-1/invites/request" {
			t.Fatalf("request stage called %s", r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"success":true}`))}, nil
	})}
	j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}
	_, err := j.RequestCandidateJoin(context.Background(), session, "workspace-1")
	if err != nil || calls != 1 {
		t.Fatalf("request stage issued %d calls; want exactly 1 (err=%v)", calls, err)
	}
}

// TestRotationJoinErrorStatusesRemainConservativeReceipts verifies 404, conflict
// and 5xx receipts still report a possibly effective attempt and never claim
// verified membership or a terminated obligation, even for a terminating code.
func TestRotationJoinErrorStatusesRemainConservativeReceipts(t *testing.T) {
	session := rotationJoinFixtureSession(time.Now())
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"not_found", http.StatusNotFound, `{"detail":{"code":"workspace_not_found"}}`},
		{"conflict", http.StatusConflict, `{"detail":{"code":"upstream_other"}}`},
		{"server_error", http.StatusInternalServerError, ``},
		{"account_deactivated", http.StatusForbidden, `{"detail":{"code":"account_deactivated"}}`},
		{"domain_restricted", http.StatusForbidden, `{"code":"domain_restricted"}`},
		{"deactivated_workspace", http.StatusBadRequest, `{"code":"deactivated_workspace"}`},
		{"unauthorized", http.StatusUnauthorized, `{}`},
		{"bad_request", http.StatusBadRequest, `{}`},
		{"rate_limit", http.StatusTooManyRequests, `{}`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tt.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tt.body))}, nil
			})}
			j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}
			result, err := j.AcceptCandidateJoin(context.Background(), session, "workspace-1")
			if err != nil || result.Success || !result.RequestSent {
				t.Fatalf("error receipt misclassified: result=%+v err=%v", result, err)
			}
			if !result.RequestMayHaveEffect {
				t.Fatalf("receipt dropped sent obligation: %+v", result)
			}
		})
	}
}

// TestRotationJoinNeverReportsProvablyUnsentAfterTransport verifies that once
// the transport was entered, no failure path reports RequestSent=false.
func TestRotationJoinNeverReportsProvablyUnsentAfterTransport(t *testing.T) {
	session := rotationJoinFixtureSession(time.Now())
	client := &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(errReader{})}, nil
	})}
	j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}
	for _, workspace := range []string{"workspace-1"} {
		result, _ := j.AcceptCandidateJoin(context.Background(), session, workspace)
		if !result.RequestSent {
			t.Fatal("transported attempt reported provably unsent")
		}
	}
}

// TestRotationJoinErrorsAreRedacted verifies adapter errors never embed the
// Personal bearer, device id, cookie values, workspace secrets, or raw upstream
// bodies, including on transport failures that may embed credentials.
func TestRotationJoinErrorsAreRedacted(t *testing.T) {
	session := rotationJoinFixtureSession(time.Now())
	secretBody := `{"detail":{"code":"invalid_request"},"token":"` + session.AccessToken + `"}`
	cases := []struct {
		name   string
		client *http.Client
	}{
		{"transport", &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
			return nil, errors.New("proxy failed for Bearer " + session.AccessToken + " and cookie " + session.Cookies[0].Value)
		})}},
		{"body", &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(secretBody))}, nil
		})}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) { return tt.client, func() {}, nil }}
			result, err := j.RequestCandidateJoin(context.Background(), session, "workspace-1")
			if result.Success {
				t.Fatal("redaction case reported success")
			}
			rendered := result.ErrorCode + " " + result.Semantic
			if err != nil {
				rendered += " " + err.Error()
			}
			for _, secret := range []string{session.AccessToken, session.Cookies[0].Value, session.DeviceID, "fixture-personal-token"} {
				if strings.Contains(rendered, secret) {
					t.Fatalf("secret leaked into adapter output: %s", rendered)
				}
			}
		})
	}
}

// Both stages must use the concrete path, sourced route template, and effective
// root Referer after the reference semantic-header helper runs.
func TestRotationJoinExactProtocolHeaders(t *testing.T) {
	for _, stage := range []string{"request", "accept"} {
		t.Run(stage, func(t *testing.T) {
			session := rotationJoinFixtureSession(time.Now())
			calls := 0
			client := &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				path := "/backend-api/accounts/workspace-1/invites/" + stage
				route := "/backend-api/accounts/{account_id}/invites/" + stage
				if r.Method != http.MethodPost || r.URL.String() != "https://chatgpt.com"+path || r.Header.Get("X-Openai-Target-Path") != path || r.Header.Get("X-Openai-Target-Route") != route {
					t.Errorf("wrong protocol target: method=%s url=%s path=%q route=%q", r.Method, r.URL, r.Header.Get("X-Openai-Target-Path"), r.Header.Get("X-Openai-Target-Route"))
				}
				if got := r.Header.Get("Referer"); got != "https://chatgpt.com/" {
					t.Errorf("wrong effective Referer: %q", got)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != "{}" {
					t.Errorf("wrong source body: %q err=%v", body, err)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
			})}
			j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) { return client, nil, nil }}
			result, err := rotationJoinTestStage(j, stage)(context.Background(), session, "workspace-1")
			if err != nil || !result.RequestSent || !result.RequestMayHaveEffect || calls != 1 {
				t.Fatalf("receipt=%+v err=%v calls=%d", result, err, calls)
			}
		})
	}
}

func TestRotationJoinBothStagesIsolateJarAndPreserveClient(t *testing.T) {
	for _, stage := range []string{"request", "accept"} {
		t.Run(stage, func(t *testing.T) {
			session := rotationJoinFixtureSession(time.Now())
			session.Cookies = append(session.Cookies, SessionCookie{Name: rotationJoinAcceptCookie, Value: "caller-stale"})
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			origin, _ := url.Parse(selectedWorkspaceOrigin)
			jar.SetCookies(origin, []*http.Cookie{{Name: "ambient", Value: "secret"}})
			redirectCalls, calls := 0, 0
			transport := &rotationJoinCountingTransport{roundTrip: func(r *http.Request) (*http.Response, error) {
				calls++
				cookies := make(map[string]string)
				for _, cookie := range r.Cookies() {
					cookies[cookie.Name] = cookie.Value
				}
				if cookies["ambient"] != "" || cookies["response-only"] != "" || cookies["__Secure-next-auth.session-token"] != "fixture-session-cookie" {
					t.Errorf("ambient or prior-attempt cookie inherited, or Personal cookie lost")
				}
				wantAccept := ""
				if stage == "accept" {
					wantAccept = "workspace-1"
				}
				if cookies[rotationJoinAcceptCookie] != wantAccept {
					t.Errorf("wrong stage selection cookie")
				}
				return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://evil.example/"}, "Set-Cookie": {"response-only=secret; Path=/"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
			}}
			client := &http.Client{Timeout: time.Second, Jar: jar, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { redirectCalls++; return nil }}
			j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) { return client, nil, nil }}
			for i := 0; i < 2; i++ {
				result, err := rotationJoinTestStage(j, stage)(context.Background(), session, "workspace-1")
				if err != nil || result.Success || !result.RequestSent || !result.RequestMayHaveEffect {
					t.Fatalf("receipt=%+v err=%v", result, err)
				}
			}
			if client.Jar != jar || client.Transport != transport || client.Timeout != time.Second || client.CheckRedirect == nil || calls != 2 || redirectCalls != 0 {
				t.Fatal("source client mutated or redirected/retried")
			}
			_ = client.CheckRedirect(nil, nil)
			if redirectCalls != 1 {
				t.Fatal("source redirect policy replaced")
			}
			cookies := jar.Cookies(origin)
			if len(cookies) != 1 || cookies[0].Name != "ambient" || cookies[0].Value != "secret" {
				t.Fatal("source jar mutated")
			}
			if session.Cookies[1].Value != "caller-stale" {
				t.Fatal("source session cookies mutated")
			}
		})
	}
}

func TestRotationJoinBothStagesReleaseOnceOnFailure(t *testing.T) {
	for _, stage := range []string{"request", "accept"} {
		for _, failure := range []string{"acquisition", "nil_client", "nil_transport", "no_timeout", "transport"} {
			t.Run(stage+"/"+failure, func(t *testing.T) {
				session := rotationJoinFixtureSession(time.Now())
				released, acquired, calls := 0, 0, 0
				client := &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("secret transport error") })}
				j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) {
					acquired++
					release := func() { released++ }
					switch failure {
					case "acquisition":
						return client, release, errors.New("secret admission error")
					case "nil_client":
						return nil, release, nil
					case "nil_transport":
						client.Transport = nil
					case "no_timeout":
						client.Timeout = 0
					}
					return client, release, nil
				}}
				result, err := rotationJoinTestStage(j, stage)(context.Background(), session, "workspace-1")
				wantSent := failure == "transport"
				wantCalls := 0
				if wantSent {
					wantCalls = 1
				}
				if err != ErrRotationJoinUnavailable || released != 1 || acquired != 1 || calls != wantCalls || result.RequestSent != wantSent || result.RequestMayHaveEffect != wantSent || result.Success {
					t.Fatalf("receipt=%+v err=%v releases=%d acquired=%d calls=%d", result, err, released, acquired, calls)
				}
			})
		}
	}
}

func TestRotationJoinBothStagesInvalidReceiptsRemainUncertain(t *testing.T) {
	for _, stage := range []string{"request", "accept"} {
		for _, receipt := range []string{"malformed", "truncated", "read_error", "partial_read", "oversized"} {
			t.Run(stage+"/"+receipt, func(t *testing.T) {
				session := rotationJoinFixtureSession(time.Now())
				secret := session.AccessToken
				var reader io.Reader
				switch receipt {
				case "malformed":
					reader = strings.NewReader("not json " + secret)
				case "truncated":
					reader = strings.NewReader(`{"token":"` + secret)
				case "read_error":
					reader = errReader{}
				case "partial_read":
					reader = io.MultiReader(strings.NewReader(`{"token":"`+secret+`"}`), errReader{})
				case "oversized":
					reader = strings.NewReader(`{"token":"` + secret + `","padding":"` + strings.Repeat("x", maxResponseBody) + `"}`)
				}
				body := &rotationJoinTrackedBody{Reader: reader}
				calls, released := 0, 0
				client := &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(*http.Request) (*http.Response, error) {
					calls++
					return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
				})}
				j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) { return client, func() { released++ }, nil }}
				result, err := rotationJoinTestStage(j, stage)(context.Background(), session, "workspace-1")
				if err != ErrRotationJoinUnavailable || result.Success || !result.RequestSent || !result.RequestMayHaveEffect || result.HTTPStatus != http.StatusOK || result.ErrorCode != "" || result.Semantic != "" || calls != 1 || released != 1 || body.closed != 1 {
					t.Fatalf("receipt=%+v err=%v calls=%d released=%d closed=%d", result, err, calls, released, body.closed)
				}
				if strings.Contains(err.Error(), secret) {
					t.Fatal("receipt leaked secret")
				}
			})
		}
	}
}

func TestRotationJoinBothStagesRejectScopeConfusionBeforeHTTP(t *testing.T) {
	for _, stage := range []string{"request", "accept"} {
		for _, invalid := range []string{"workspace", "expired_session", "expired_token", "client_id", "chatgpt_account_id", "chatgpt_plan_type", "scp", "amr", "required"} {
			t.Run(stage+"/"+invalid, func(t *testing.T) {
				now := time.Now()
				session := rotationJoinFixtureSession(now)
				switch invalid {
				case "workspace":
					session.AccessToken = workspaceFixtureTokenWithClaims(map[string]any{"exp": now.Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "workspace-1", "chatgpt_plan_type": "k12", "scp": []string{"organization.read"}}})
				case "expired_session":
					session.ExpiresAt = now.Add(-time.Minute)
				case "expired_token":
					session.AccessToken = personalFixtureToken(now, func(claims map[string]any) { claims["exp"] = now.Add(-time.Minute).Unix() })
				default:
					session.AccessToken = personalFixtureToken(now, func(claims map[string]any) {
						switch invalid {
						case "scp":
							claims[invalid] = []string{"organization.read"}
						case "amr":
							claims[invalid] = []string{"password"}
						case "required":
							claims[invalid] = false
						default:
							claims[invalid] = "conflicting-workspace-claim"
						}
					})
				}
				calls, acquired := 0, 0
				client := &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("must not send") })}
				j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) { acquired++; return client, nil, nil }}
				result, err := rotationJoinTestStage(j, stage)(context.Background(), session, "workspace-1")
				if err != ErrRotationJoinUnavailable || result.RequestSent || result.RequestMayHaveEffect || result.Success || calls != 0 || acquired != 0 {
					t.Fatalf("invalid session admitted: receipt=%+v err=%v calls=%d acquired=%d", result, err, calls, acquired)
				}
			})
		}
	}
}

func TestRotationJoinBothStagesEveryHTTPStatusPreservesSentObligation(t *testing.T) {
	for _, stage := range []string{"request", "accept"} {
		for _, status := range []int{200, 202, 204, 302, 400, 401, 403, 404, 409, 429, 500} {
			for _, receipt := range []string{"", `{}`, `{"success":true}`, `{"success":false}`, `{"code":"workspace_not_found"}`, `{"code":"domain_restricted"}`, `{"code":"deactivated_workspace"}`} {
				session := rotationJoinFixtureSession(time.Now())
				calls := 0
				client := &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(*http.Request) (*http.Response, error) {
					calls++
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(receipt))}, nil
				})}
				j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) { return client, nil, nil }}
				result, err := rotationJoinTestStage(j, stage)(context.Background(), session, "workspace-1")
				if err != nil || !result.RequestSent || !result.RequestMayHaveEffect || result.Success != (status >= 200 && status < 300) || result.HTTPStatus != status || calls != 1 {
					t.Errorf("stage=%s status=%d body=%q receipt=%+v err=%v calls=%d", stage, status, receipt, result, err, calls)
				}
			}
		}
	}
}

func TestRotationJoinBothStagesRejectAmbiguousOrInvalidCookiesBeforeAcquisition(t *testing.T) {
	const sessionName = "__Secure-next-auth.session-token"
	for _, stage := range []string{"request", "accept"} {
		cases := []struct {
			name    string
			cookies []SessionCookie
		}{
			{"padded_B_before_A", []SessionCookie{{Name: " " + sessionName, Value: "session-B"}, {Name: sessionName, Value: "session-A"}}},
			{"A_before_padded_B", []SessionCookie{{Name: sessionName, Value: "session-A"}, {Name: " " + sessionName, Value: "session-B"}}},
			{"conflicting_duplicates", []SessionCookie{{Name: sessionName, Value: "session-B"}, {Name: sessionName, Value: "session-A"}}},
			{"reverse_conflicting_duplicates", []SessionCookie{{Name: sessionName, Value: "session-A"}, {Name: sessionName, Value: "session-B"}}},
			{"identical_duplicates", []SessionCookie{{Name: sessionName, Value: "session-A"}, {Name: sessionName, Value: "session-A"}}},
		}
		for _, name := range []string{"", " padded", "padded ", "\tpadded", "padded\u00a0", "invalid name", "invalid;name", "invalid=name", "invalid\nname"} {
			cases = append(cases, struct {
				name    string
				cookies []SessionCookie
			}{"invalid_name_" + name, []SessionCookie{{Name: sessionName, Value: "session-A"}, {Name: name, Value: "other"}}})
		}
		for _, value := range []string{"invalid;value", "invalid\rvalue", "invalid\nvalue", "invalid\x00value", "invalid\"value", "invalid\\value", "invalid\u00a0value"} {
			cases = append(cases, struct {
				name    string
				cookies []SessionCookie
			}{"invalid_value_" + value, []SessionCookie{{Name: sessionName, Value: "session-A"}, {Name: "other", Value: value}}})
		}
		for _, name := range []string{"other", rotationJoinAcceptCookie, "_account", "oai-workspace"} {
			cases = append(cases, struct {
				name    string
				cookies []SessionCookie
			}{"duplicate_" + name, []SessionCookie{{Name: sessionName, Value: "session-A"}, {Name: name, Value: "same"}, {Name: name, Value: "same"}}})
		}
		for _, tt := range cases {
			t.Run(stage+"/"+tt.name, func(t *testing.T) {
				session := rotationJoinFixtureSession(time.Now())
				session.Cookies = tt.cookies
				original := session
				original.Cookies = append([]SessionCookie(nil), session.Cookies...)
				acquired, calls := 0, 0
				client := &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(*http.Request) (*http.Response, error) {
					calls++
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
				})}
				j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) { acquired++; return client, nil, nil }}
				result, err := rotationJoinTestStage(j, stage)(context.Background(), session, "workspace-1")
				if err != ErrRotationJoinUnavailable || result != (JoinAttemptResult{}) || acquired != 0 || calls != 0 {
					t.Errorf("ambiguous/invalid cookies admitted: receipt=%+v err=%v acquired=%d calls=%d", result, err, acquired, calls)
				}
				if !reflect.DeepEqual(session, original) {
					t.Error("original session changed")
				}
			})
		}
	}
}

func TestRotationJoinBothStagesPreserveValidCookieIdentity(t *testing.T) {
	for _, stage := range []string{"request", "accept"} {
		t.Run(stage, func(t *testing.T) {
			session := rotationJoinFixtureSession(time.Now())
			session.Cookies[0].Value = " session-A=exact "
			session.Cookies = append(session.Cookies, SessionCookie{Name: "exact.name", Value: " value=unchanged "})
			original := session
			original.Cookies = append([]SessionCookie(nil), session.Cookies...)
			calls := 0
			client := &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				for _, want := range session.Cookies {
					got, err := r.Cookie(want.Name)
					if err != nil || got.Value != want.Value {
						t.Errorf("cookie identity rewritten for %q: got=%v err=%v", want.Name, got, err)
					}
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
			})}
			j := OfficialRotationCandidateJoiner{Client: func(context.Context) (*http.Client, func(), error) { return client, nil, nil }}
			result, err := rotationJoinTestStage(j, stage)(context.Background(), session, "workspace-1")
			if err != nil || !result.RequestSent || !result.RequestMayHaveEffect || calls != 1 {
				t.Fatalf("valid cookies rejected: receipt=%+v err=%v calls=%d", result, err, calls)
			}
			if !reflect.DeepEqual(session, original) {
				t.Error("original session changed")
			}
		})
	}
}

func rotationJoinTestStage(j OfficialRotationCandidateJoiner, stage string) func(context.Context, PersonalSession, string) (JoinAttemptResult, error) {
	if stage == "accept" {
		return j.AcceptCandidateJoin
	}
	return j.RequestCandidateJoin
}

type rotationJoinCountingTransport struct{ roundTrip rotationJoinTransport }

func (t *rotationJoinCountingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.roundTrip(r)
}

type rotationJoinTrackedBody struct {
	io.Reader
	closed int
}

func (b *rotationJoinTrackedBody) Close() error { b.closed++; return nil }

type rotationJoinTransport func(*http.Request) (*http.Response, error)

func (f rotationJoinTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func rotationJoinFixtureSession(now time.Time) PersonalSession {
	token := personalFixtureToken(now, nil)
	return PersonalSession{
		AccessToken: token,
		DeviceID:    "fixture-device",
		Cookies: []SessionCookie{
			{Name: "__Secure-next-auth.session-token", Value: "fixture-session-cookie"},
		},
		ExpiresAt: now.Add(time.Hour),
	}
}
