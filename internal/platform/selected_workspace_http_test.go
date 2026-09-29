package platform

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type selectedTransport struct {
	mu     sync.Mutex
	calls  []string
	answer func(*http.Request) (int, string, http.Header)
}

func (t *selectedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet || req.URL.Scheme != "https" || req.URL.Host != "chatgpt.com" || !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer ") ||
		!strings.Contains(req.Header.Get("Cookie"), "__Secure-next-auth.session-token=saved-cookie") ||
		(strings.HasSuffix(req.URL.Path, "/invites") && req.Header.Get("Oai-Session-Id") == "") {
		panic("unexpected platform operation or credential")
	}
	t.mu.Lock()
	t.calls = append(t.calls, req.URL.String())
	t.mu.Unlock()
	status, body, headers := t.answer(req)
	return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}
func workspaceFixtureToken(workspaceID string) string {
	payload := fmt.Sprintf(`{"exp":%d,"https://api.openai.com/auth":{"chatgpt_account_id":%q}}`, time.Now().Add(2*time.Hour).Unix(), workspaceID)
	return "header." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".signature"
}
func selectedSession() WorkspaceAccess {
	return WorkspaceAccess{AccessToken: workspaceFixtureToken("canonical-space"), WorkspaceID: "canonical-space", DeviceID: "saved-device", SessionID: uuid.NewString(), Cookies: []SessionCookie{
		{Name: "__Secure-next-auth.session-token", Value: "saved-cookie"}, {Name: "_account", Value: "canonical-space"}, {Name: "oai-workspace", Value: "canonical-space"}}, ExpiresAt: time.Now().Add(time.Hour)}
}
func readerFixture(t *selectedTransport) OfficialSelectedWorkspaceReader {
	return OfficialSelectedWorkspaceReader{Client: func(context.Context) (*http.Client, func(), error) {
		return &http.Client{Transport: t, Timeout: time.Second}, func() {}, nil
	}}
}
func selectedOK(req *http.Request) (int, string, http.Header) {
	path := req.URL.Path
	query := req.URL.Query()
	switch {
	case path == "/backend-api/subscriptions" && query.Get("account_id") == "canonical-space":
		return 200, `{"active_until":"2027-11-02T00:00:00Z","seats_in_use":4,"seats_entitled":3,"seat_capacity":[{"type":"default","paid":3}]}`, nil
	case path == "/backend-api/accounts/canonical-space/users/seat_type_counts":
		return 200, `{"seat_type_counts":{"default":2,"usage_based":0,"automation":0,"prolite":0}}`, nil
	case path == "/backend-api/accounts/canonical-space/users" && query.Get("offset") == "0" && query.Get("limit") == "100" && query.Has("query"):
		return 200, `{"total":2,"limit":100,"offset":0,"items":[{"id":"user-a","email":"a@example.test","role":"owner"}]}`, nil
	case path == "/backend-api/accounts/canonical-space/users" && query.Get("offset") == "1":
		return 200, `{"total":2,"limit":100,"offset":1,"items":[{"id":"user-b","email":"b@example.test","role":"standard-user"}]}`, nil
	case path == "/backend-api/accounts/canonical-space/invites" && query.Get("offset") == "0" && query.Get("limit") == "100" && req.Header.Get("chatgpt-account-id") == "canonical-space":
		return 200, `{"total":2,"limit":100,"offset":0,"items":[{"email_address":"c@example.test","seat_type":"default","status":2}]}`, nil
	case path == "/backend-api/accounts/canonical-space/invites" && query.Get("offset") == "1":
		return 200, `{"total":2,"limit":100,"offset":1,"items":[{"email":"d@example.test","status":"pending"}]}`, nil
	default:
		panic("unexpected endpoint: " + req.URL.String())
	}
}
func TestOfficialSelectedWorkspaceReaderPagedFactsAndReadOnlyBoundary(t *testing.T) {
	transport := &selectedTransport{answer: selectedOK}
	facts, err := readerFixture(transport).VerifySelectedWorkspace(context.Background(), selectedSession(), "mother-id", "canonical-space")
	if err != nil || facts.Permission != "read" || facts.Result.Outcome != OutcomeOperational || facts.Result.Completeness != Complete ||
		facts.Result.SeatLimit == nil || *facts.Result.SeatLimit != 3 || facts.Result.MemberCount == nil || *facts.Result.MemberCount != 2 ||
		facts.Result.PendingInviteCount == nil || *facts.Result.PendingInviteCount != 2 || len(facts.Result.Members) != 4 || len(facts.Sources) != 4 {
		t.Fatalf("paired/paged fact result=%+v err=%v", facts, err)
	}
	for _, evidence := range facts.Sources {
		if evidence.Permission != "read" || evidence.Completeness != Complete || evidence.ObservedAt.IsZero() {
			t.Fatalf("source missing: %+v", evidence)
		}
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.calls) != 6 {
		t.Fatalf("expected six GETs, got %v", transport.calls)
	}
}
func TestOfficialSelectedWorkspaceReaderRejectsPartialForbiddenAndRedirect(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		override   func(*http.Request) (int, string, http.Header)
		status     Outcome
		permission string
	}{
		{"incomplete_invite_page", func(req *http.Request) (int, string, http.Header) {
			if strings.Contains(req.URL.Path, "/invites") && req.URL.Query().Get("offset") == "1" {
				return 200, `{"total":2,"limit":100,"offset":1,"items":[]}`, nil
			}
			return selectedOK(req)
		}, OutcomeIncomplete, "unknown"},
		{"forbidden_members", func(req *http.Request) (int, string, http.Header) {
			if strings.HasSuffix(req.URL.Path, "/users") {
				return 403, `{}`, nil
			}
			return selectedOK(req)
		}, OutcomeForbidden, "denied"},
		{"redirect_subscriptions", func(req *http.Request) (int, string, http.Header) {
			if req.URL.Path == "/backend-api/subscriptions" {
				return 302, "", http.Header{"Location": []string{"https://attacker.example/steal"}}
			}
			return selectedOK(req)
		}, OutcomeIncomplete, "unknown"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			transport := &selectedTransport{answer: scenario.override}
			facts, err := readerFixture(transport).VerifySelectedWorkspace(context.Background(), selectedSession(), "mother-id", "canonical-space")
			if err != nil || facts.Result.Outcome != scenario.status || facts.Permission != scenario.permission || facts.Result.ActiveUntil != nil || facts.Result.SeatLimit != nil || len(facts.Result.Members) != 0 || len(facts.Sources) != 4 {
				t.Fatalf("partial/denied facts=%+v err=%v", facts, err)
			}
		})
	}
}
func TestOfficialSelectedWorkspaceReaderRejectsInvalidPersonalSessionWithoutIO(t *testing.T) {
	transport := &selectedTransport{answer: selectedOK}
	facts, err := readerFixture(transport).VerifySelectedWorkspace(context.Background(), WorkspaceAccess{}, "mother-id", "canonical-space")
	if err != nil || facts.Permission != "unknown" || len(transport.calls) != 0 {
		t.Fatalf("invalid session used transport: %+v %v", facts, err)
	}
}
