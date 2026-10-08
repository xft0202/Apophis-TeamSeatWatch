package platform

import (
	"context"
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

func TestOfficialPersonalMembershipReaderUsesExactReadOnlyRoute(t *testing.T) {
	session := rotationJoinFixtureSession(time.Now())
	body, err := json.Marshal(map[string]any{
		"complete": true,
		"members":  []Member{{Kind: "member", PlatformMemberID: "member-1", Identifier: "candidate@example.test", Status: "active", SeatType: "prolite"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.String() != "https://chatgpt.com/workspaces/workspace-1/members" || r.URL.RawQuery != "" {
			t.Fatalf("unexpected membership request: %s %s", r.Method, r.URL)
		}
		if _, _, ok := r.BasicAuth(); ok {
			t.Fatal("membership reader reused Basic/password auth")
		}
		if r.Header.Get("Authorization") != "Bearer "+session.AccessToken {
			t.Fatal("Personal bearer missing")
		}
		if _, err := r.Cookie("__Secure-next-auth.session-token"); err != nil {
			t.Fatal("saved Personal cookie missing")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})}
	reader := OfficialPersonalMembershipReader{Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}
	result, err := reader.ReadMembers(context.Background(), session, "workspace-1")
	if err != nil || calls != 1 || result.Outcome != OutcomeOperational || result.Completeness != Complete || len(result.Members) != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls)
	}
}

func TestOfficialPersonalMembershipReaderFailsClosedForPartialOrUnauthorized(t *testing.T) {
	session := rotationJoinFixtureSession(time.Now())
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   Outcome
	}{
		{name: "partial", status: http.StatusOK, body: `{"complete":false,"members":[]}`, want: OutcomeOperational},
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"error_code":"auth_error"}`, want: OutcomeUnauthorized},
		{name: "malformed", status: http.StatusOK, body: `{`, want: OutcomeIncomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := OfficialPersonalMembershipReader{Client: func(context.Context) (*http.Client, func(), error) {
				return &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
				})}, func() {}, nil
			}}
			result, err := reader.ReadMembers(context.Background(), session, "workspace-1")
			if err != nil || result.Outcome != tc.want || result.Completeness == Complete {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestOfficialPersonalMembershipReaderDoesNotFollowRedirect(t *testing.T) {
	session := rotationJoinFixtureSession(time.Now())
	calls := 0
	reader := OfficialPersonalMembershipReader{Client: func(context.Context) (*http.Client, func(), error) {
		return &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://evil.example/"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		})}, func() {}, nil
	}}
	result, err := reader.ReadMembers(context.Background(), session, "workspace-1")
	if err != nil || calls != 1 || result.Outcome != OutcomeIncomplete {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls)
	}
	if errors.Is(err, ErrPersonalIdentityUnavailable) {
		t.Fatal("membership reader returned identity error")
	}
}

func TestOfficialPersonalMembershipReaderIsolationAndBoundedAmbiguity(t *testing.T) {
	for _, mode := range []string{"ambient", "duplicate_key", "wrong_workspace", "oversize", "transport", "expired", "unsafe_workspace", "workspace_hint"} {
		t.Run(mode, func(t *testing.T) {
			session := rotationJoinFixtureSession(time.Now())
			jar, _ := cookiejar.New(nil)
			origin, _ := url.Parse(officialChatBase + "/")
			jar.SetCookies(origin, []*http.Cookie{{Name: "ambient-secret", Value: "ambient-cookie"}})
			calls, releases := 0, 0
			workspace := "workspace-1"
			switch mode {
			case "expired":
				session.ExpiresAt = time.Now().Add(-time.Second)
			case "unsafe_workspace":
				workspace = "workspace-1/other"
			case "workspace_hint":
				session.Cookies = append(session.Cookies, SessionCookie{Name: "_account", Value: "wrong-workspace"})
			}
			reader := OfficialPersonalMembershipReader{Client: func(context.Context) (*http.Client, func(), error) {
				return &http.Client{Jar: jar, Timeout: time.Second, Transport: rotationJoinTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					if _, err := r.Cookie("ambient-secret"); err == nil {
						t.Fatal("ambient cookies inherited")
					}
					if mode == "transport" {
						return nil, errors.New(session.AccessToken + " secret-cookie")
					}
					body := `{"complete":true,"members":[]}`
					switch mode {
					case "duplicate_key":
						body = `{"complete":false,"complete":true,"members":[]}`
					case "wrong_workspace":
						body = `{"complete":true,"workspace_id":"other-workspace","members":[]}`
					case "oversize":
						body = strings.Repeat("x", maxResponseBody+1)
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
				})}, func() { releases++ }, nil
			}}
			result, err := reader.ReadMembers(context.Background(), session, workspace)
			if mode == "ambient" {
				if err != nil || result.Completeness != Complete {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			} else if result.Completeness == Complete {
				t.Fatal("ambiguous membership accepted")
			}
			if err != nil && strings.Contains(err.Error(), session.AccessToken) {
				t.Fatal("secret transport diagnostic escaped")
			}
			if calls > 1 || releases > 1 {
				t.Fatal("reader retried or double-released")
			}
			if (mode == "expired" || mode == "unsafe_workspace" || mode == "workspace_hint") && calls != 0 {
				t.Fatal("inadmissible read sent")
			}
		})
	}
}

func TestOfficialPersonalMembershipReaderCaseFoldedKeys(t *testing.T) {
	for _, body := range []string{
		`{"complete":false,"Complete":true,"members":[]}`,
		`{"complete":true,"members":[],"Members":[]}`,
		`{"complete":true,"workspace_id":"other","Workspace_ID":"workspace-1","members":[]}`,
		`{"complete":true,"members":[{"kind":"member","platform_member_id":"one","identifier":"candidate@example.test","status":"pending","Status":"active","seat_type":"prolite"}]}`,
		`{"complete":true,"members":[{"kind":"member","platform_member_id":"one","Platform_Member_ID":"two","identifier":"candidate@example.test","status":"active","seat_type":"prolite"}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			session := rotationJoinFixtureSession(time.Now())
			reader := OfficialPersonalMembershipReader{Client: func(context.Context) (*http.Client, func(), error) {
				return &http.Client{Timeout: time.Second, Transport: rotationJoinTransport(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
				})}, nil, nil
			}}
			result, err := reader.ReadMembers(context.Background(), session, "workspace-1")
			if err != nil || result.Outcome != OutcomeIncomplete || result.Completeness == Complete {
				t.Fatalf("ambiguous reader result=%+v err=%v", result, err)
			}
		})
	}
}
