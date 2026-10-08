package platform

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type invitationTransport func(*http.Request) (*http.Response, error)

func (f invitationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWorkspaceInvitationPreservesSubscriptionRejection(t *testing.T) {
	for _, scenario := range []struct{ name, body, code string }{
		{"delinquent", `{"detail":{"code":"workspace_subscription_delinquent","message":"Team has a delinquent subscription"}}`, "workspace_subscription_delinquent"},
		{"unknown", `{"detail":{"code":"private-provider-value","message":"private response"}}`, "invitation_rejected"},
		{"malformed", `<html>private response</html>`, "invitation_rejected"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			posts := 0
			transport := invitationTransport(func(r *http.Request) (*http.Response, error) {
				status, body := 200, `{"total":0,"limit":100,"offset":0,"items":[]}`
				if r.URL.Path == "/backend-api/subscriptions" {
					body = `{"active_until":"2027-11-02T00:00:00Z","seats_in_use":0,"seats_entitled":9,"seat_capacity":[{"type":"default","paid":2},{"type":"prolite","paid":9}]}`
				}
				if strings.HasSuffix(r.URL.Path, "/seat_type_counts") {
					body = `{"seat_type_counts":{"default":2,"usage_based":0,"automation":0,"prolite":0}}`
				}
				if r.Method == http.MethodPost {
					posts++
					status, body = 401, scenario.body
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
			})
			result, err := (WorkspaceInvitation{Client: &http.Client{Transport: transport, Timeout: time.Second}, Access: selectedSession(), Workspace: "canonical-space", RecipientSubject: "candidate"}).Send(context.Background(), "candidate@example.test")
			if err != nil || posts != 1 || result.HTTPStatus != 401 || result.Success || !result.RequestSent || result.RequestMayHaveEffect || result.ErrorCode != scenario.code || NormalizeDiagnostic(result.ErrorCode) != scenario.code {
				t.Fatalf("subscription rejection lost or misclassified: %+v err=%v posts=%d", result, err, posts)
			}
		})
	}
}

func TestWorkspaceInvitationRequiresPremiumRosterEvidence(t *testing.T) {
	for _, scenario := range []struct {
		name, existing, after string
		status                int
		success, sent         bool
	}{
		{"reuse premium invitation", "prolite", "", 200, true, false},
		{"reject ordinary invitation", "default", "", 200, false, false},
		{"HTTP success without roster proof", "", "", 200, false, true},
		{"confirmed new premium invitation", "", "prolite", 200, true, true},
		{"confirmed wrong seat after send", "", "default", 200, false, true},
		{"platform permission rejection", "", "", 403, false, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			posts := 0
			access := selectedSession()
			transport := invitationTransport(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Authorization") != "Bearer "+access.AccessToken || r.URL.Host != "chatgpt.com" {
					t.Fatal("wrong mother or host")
				}
				body := `{"total":0,"limit":100,"offset":0,"items":[]}`
				status := 200
				if r.Method == http.MethodPost {
					posts++
					status = scenario.status
					body = `{"account_invites":[]}`
				} else if r.URL.Path == "/backend-api/subscriptions" {
					body = `{"active_until":"2027-11-02T00:00:00Z","seats_in_use":0,"seats_entitled":9,"seat_capacity":[{"type":"default","paid":2},{"type":"prolite","paid":9}]}`
				} else if strings.HasSuffix(r.URL.Path, "/seat_type_counts") {
					body = `{"seat_type_counts":{"default":2,"usage_based":0,"automation":0,"prolite":0}}`
				} else if strings.HasSuffix(r.URL.Path, "/invites") {
					seat := scenario.existing
					if posts > 0 {
						seat = scenario.after
					}
					if seat != "" {
						body = `{"total":1,"limit":100,"offset":0,"items":[{"email_address":"candidate@example.test","status":2,"seat_type":"` + seat + `"}]}`
					}
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
			})
			adapter := WorkspaceInvitation{Client: &http.Client{Transport: transport, Timeout: time.Second}, Access: access, Workspace: "canonical-space", RecipientSubject: "candidate"}
			result, err := adapter.Send(context.Background(), "candidate@example.test")
			if err != nil || result.Success != scenario.success || result.RequestSent != scenario.sent {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if result.RequestMayHaveEffect && posts != 1 {
				t.Fatal("effect flag without request")
			}
			if scenario.status == 403 && !result.RequestMayHaveEffect {
				t.Fatal("403 must preserve uncertain delivery for read-only reconciliation")
			}
		})
	}
}

func TestWorkspaceInvitationMembershipSeparatesPendingAndJoined(t *testing.T) {
	for _, scenario := range []struct {
		name, members, invites    string
		complete, joined, pending bool
		seat                      string
	}{
		{"pending premium", "", `{"email_address":"candidate@example.test","status":2,"seat_type":"prolite"}`, true, false, true, "prolite"},
		{"pending ordinary", "", `{"email_address":"candidate@example.test","status":2,"seat_type":"default"}`, true, false, true, "default"},
		{"joined premium", `{"id":"candidate","email":"candidate@example.test","role":"standard-user","seat_type":"prolite"}`, "", true, true, false, "prolite"},
		{"conflicting records", `{"id":"candidate","email":"candidate@example.test","role":"standard-user","seat_type":"prolite"}`, `{"email_address":"candidate@example.test","status":2,"seat_type":"prolite"}`, false, true, false, "prolite"},
		{"absent", "", "", true, false, false, ""},
		{"recipient email undisclosed", `{"id":"candidate","role":"standard-user","seat_type":"prolite"}`, "", true, true, false, "prolite"},
		{"email without matching authenticated user", `{"id":"another-user","email":"candidate@example.test","role":"standard-user","seat_type":"prolite"}`, "", true, false, false, ""},
		{"deactivated recipient is not joined", `{"id":"candidate","role":"standard-user","seat_type":"prolite","deactivated_time":"2026-10-07T00:00:00Z"}`, "", false, false, false, ""},
		{"premium invitation beside unidentified member", `{"id":"unidentified-member","role":"standard-user","seat_type":"prolite"}`, `{"email_address":"candidate@example.test","status":2,"seat_type":"prolite"}`, true, false, true, "prolite"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			transport := invitationTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet {
					t.Fatal("membership verification attempted a mutation")
				}
				items := scenario.members
				if strings.HasSuffix(r.URL.Path, "/invites") {
					items = scenario.invites
				}
				total := "0"
				if items != "" {
					total = "1"
				}
				body := `{"total":` + total + `,"limit":100,"offset":0,"items":[` + items + `]}`
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
			})
			result, err := (WorkspaceInvitation{Client: &http.Client{Transport: transport}, Access: selectedSession(), Workspace: "canonical-space", RecipientSubject: "candidate"}).Membership(context.Background(), "candidate@example.test")
			if result.Complete != scenario.complete || result.Present != scenario.joined || result.InvitationPresent != scenario.pending || (scenario.complete && err != nil) {
				t.Fatalf("membership evidence=%+v error=%v", result, err)
			}
			if scenario.pending && result.InvitationSeatType != scenario.seat || scenario.joined && result.SeatType != scenario.seat {
				t.Fatal("seat class lost")
			}
		})
	}
}

func TestWorkspaceInvitationRejectsFullPremiumSeatsBeforePost(t *testing.T) {
	for _, scenario := range []struct {
		name                      string
		opened, occupied, pending int
		wantPost                  bool
	}{
		{"nine members fill nine seats", 9, 9, 0, false},
		{"pending invite reserves final seat", 9, 8, 1, false},
		{"one free seat permits invitation", 9, 8, 0, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			posts := 0
			transport := invitationTransport(func(r *http.Request) (*http.Response, error) {
				body := `{"total":0,"limit":100,"offset":0,"items":[]}`
				switch {
				case r.Method == http.MethodPost:
					posts++
				case r.URL.Path == "/backend-api/subscriptions":
					body = fmt.Sprintf(`{"active_until":"2027-11-02T00:00:00Z","seats_in_use":%d,"seats_entitled":%d,"seat_capacity":[{"type":"default","paid":2},{"type":"prolite","paid":%d}]}`, scenario.occupied+2, scenario.opened+2, scenario.opened)
				case strings.HasSuffix(r.URL.Path, "/seat_type_counts"):
					body = fmt.Sprintf(`{"seat_type_counts":{"default":2,"usage_based":0,"automation":0,"prolite":%d}}`, scenario.occupied)
				case strings.HasSuffix(r.URL.Path, "/invites") && scenario.pending > 0:
					body = `{"total":1,"limit":100,"offset":0,"items":[{"email_address":"other@example.test","status":2,"seat_type":"prolite"}]}`
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
			})
			result, err := (WorkspaceInvitation{Client: &http.Client{Transport: transport}, Access: selectedSession(), Workspace: "canonical-space"}).Send(context.Background(), "new@example.test")
			if err != nil {
				t.Fatal(err)
			}
			if (posts == 1) != scenario.wantPost {
				t.Fatalf("posts=%d result=%+v", posts, result)
			}
			if !scenario.wantPost && (result.RequestSent || result.RequestMayHaveEffect || result.ErrorCode != "premium_capacity_exceeded") {
				t.Fatalf("full capacity must remain explicitly unsent: %+v", result)
			}
		})
	}
}

func TestWorkspaceInvitationReusesPremiumRelationshipAtFullCapacity(t *testing.T) {
	for _, kind := range []string{"member", "pending_invite"} {
		t.Run(kind, func(t *testing.T) {
			posts := 0
			transport := invitationTransport(func(r *http.Request) (*http.Response, error) {
				body := `{"total":0,"limit":100,"offset":0,"items":[]}`
				if r.Method == http.MethodPost {
					posts++
				} else if kind == "member" && strings.HasSuffix(r.URL.Path, "/users") {
					body = `{"total":1,"limit":100,"offset":0,"items":[{"id":"existing-member","email":"existing@example.test","role":"standard-user","seat_type":"prolite"}]}`
				} else if kind == "pending_invite" && strings.HasSuffix(r.URL.Path, "/invites") {
					body = `{"total":1,"limit":100,"offset":0,"items":[{"email_address":"existing@example.test","status":2,"seat_type":"prolite"}]}`
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
			})
			result, err := (WorkspaceInvitation{Client: &http.Client{Transport: transport}, Access: selectedSession(), Workspace: "canonical-space"}).Send(context.Background(), "existing@example.test")
			if err != nil || !result.Success || result.RequestSent || result.RequestMayHaveEffect || posts != 0 {
				t.Fatalf("existing premium relationship must continue without a new invitation: result=%+v posts=%d error=%v", result, posts, err)
			}
		})
	}
}
