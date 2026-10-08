package platform

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"
)

// This contract is sourced from the reference members_remove.go: a selected
// Workspace bearer deletes exactly the frozen member ID. A transport receipt
// never contains or implies an absent_verified business result.
func TestRotationRemovalUsesOnlyFrozenWorkspaceBearerAndMember(t *testing.T) {
	calls := 0
	client := &http.Client{Timeout: time.Second, Transport: rotationRemovalTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodDelete || r.URL.String() != "https://chatgpt.com/backend-api/accounts/space-1/users/member-1" {
			t.Fatalf("unexpected mutation: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer fixture-workspace-token" || r.Header.Get("Chatgpt-Account-Id") != "space-1" || r.Header.Get("Oai-Device-Id") != "fixture-device" {
			t.Fatal("selected Workspace scope or authenticated device missing")
		}
		if _, _, basic := r.BasicAuth(); basic || r.Body != nil || r.Header.Get("Referer") != "https://chatgpt.com/admin/members" {
			t.Fatal("password gateway or invented request body used")
		}
		return &http.Response{StatusCode: http.StatusNoContent, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	r := OfficialWorkspaceMemberRemover{Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}
	access := WorkspaceAccess{AccessToken: "fixture-workspace-token", WorkspaceID: "space-1", DeviceID: "fixture-device", ExpiresAt: time.Now().Add(time.Hour)}
	result, err := r.RemoveWorkspaceMember(context.Background(), access, "space-1", "member-1", "local-request-1")
	if err != nil || result.HTTPStatus != http.StatusNoContent || calls != 1 {
		t.Fatalf("receipt=%+v err=%v calls=%d", result, err, calls)
	}
}

type rotationRemovalTransport func(*http.Request) (*http.Response, error)

func (f rotationRemovalTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRotationRemovalSnapshotRejectsDuplicateRemoteIdentities(t *testing.T) {
	client := &http.Client{Timeout: time.Second, Transport: rotationRemovalTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/backend-api/accounts/space-1/users" || r.URL.Query().Get("offset") != "0" || r.URL.Query().Get("limit") != "100" {
			t.Fatalf("unexpected read: %s %s", r.Method, r.URL)
		}
		body := `{"total":2,"limit":100,"offset":0,"items":[{"id":"member-1","account_user_id":"acct-dup","email":"one@example.test","role":"member","seat_type":"prolite"},{"id":"member-2","account_user_id":"acct-dup","email":"two@example.test","role":"member","seat_type":"prolite"}]}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	r := OfficialWorkspaceMemberRemover{Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}
	access := WorkspaceAccess{AccessToken: "fixture-workspace-token", WorkspaceID: "space-1", DeviceID: "fixture-device", ExpiresAt: time.Now().Add(time.Hour)}
	if snapshot, err := r.SnapshotWorkspaceMembers(context.Background(), access, "space-1"); err == nil || snapshot.DeclaredMemberCount != 0 {
		t.Fatalf("duplicate account_user_id accepted: snapshot=%+v err=%v", snapshot, err)
	}
}

func TestRotationRemovalNeverMixesWorkspaceBearerWithCookies(t *testing.T) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("https://chatgpt.com")
	jar.SetCookies(origin, []*http.Cookie{{Name: "__Secure-next-auth.session-token", Value: "unrelated-jar-identity"}})
	calls := 0
	client := &http.Client{Timeout: time.Second, Jar: jar, Transport: rotationRemovalTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "Bearer fixture-workspace-token" {
			t.Errorf("mixed or wrong workspace identity: method=%s cookie_present=%t", req.Method, req.Header.Get("Cookie") != "")
		}
		body := `{"total":1,"offset":0,"limit":100,"items":[{"id":"member-1","email":"one@example.test","role":"member","seat_type":"prolite"}]}`
		status := http.StatusOK
		if req.Method == http.MethodDelete {
			status = http.StatusNoContent
			body = ""
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	remover := OfficialWorkspaceMemberRemover{Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}
	access := WorkspaceAccess{AccessToken: "fixture-workspace-token", WorkspaceID: "space-1", DeviceID: "fixture-device", ExpiresAt: time.Now().Add(time.Hour), Cookies: []SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "stale-personal-identity"}}}
	if _, err = remover.SnapshotWorkspaceMembers(context.Background(), access, "space-1"); err != nil {
		t.Fatal(err)
	}
	if _, err = remover.RemoveWorkspaceMember(context.Background(), access, "space-1", "member-1", "original-request"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("unexpected calls=%d", calls)
	}
}

func TestRotationRemovalTransportUncertaintyKeepsMayHaveReachedReceipt(t *testing.T) {
	client := &http.Client{Timeout: time.Second, Transport: rotationRemovalTransport(func(r *http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})}
	r := OfficialWorkspaceMemberRemover{Client: func(context.Context) (*http.Client, func(), error) { return client, func() {}, nil }}
	access := WorkspaceAccess{AccessToken: "fixture-workspace-token", WorkspaceID: "space-1", DeviceID: "fixture-device", ExpiresAt: time.Now().Add(time.Hour)}
	result, err := r.RemoveWorkspaceMember(context.Background(), access, "space-1", "member-1", "local-request-1")
	if err == nil || !result.Retryable || !result.RequestMayHaveEffect || result.Accepted {
		t.Fatalf("uncertain transport not retained: result=%+v err=%v", result, err)
	}
}
