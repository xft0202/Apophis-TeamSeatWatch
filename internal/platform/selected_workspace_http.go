package platform

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const selectedWorkspaceOrigin = "https://chatgpt.com"
const selectedPageLimit = 100
const selectedMaxEntries = 1000

// OfficialSelectedWorkspaceReader uses only the four sourced GET capabilities
// with an explicitly exchanged Workspace bearer. Client must hand it an admitted
// egress-lease client; redirects and cookie jars are disabled on the copy.
type OfficialSelectedWorkspaceReader struct{ Client DiscoveryClient }

func (OfficialSelectedWorkspaceReader) Source() string { return "official_readonly" }

func (r OfficialSelectedWorkspaceReader) VerifySelectedWorkspace(ctx context.Context, access WorkspaceAccess, _, workspaceID string) (SelectedWorkspaceFacts, error) {
	facts := SelectedWorkspaceFacts{Permission: "unknown", Result: Result{ObservedAt: time.Now().UTC(), Completeness: Unknown, Outcome: OutcomeIncomplete}, Sources: []ReadEvidence{}}
	if r.Client == nil || !ValidateWorkspaceAccess(access, workspaceID, time.Now()) ||
		strings.TrimSpace(workspaceID) == "" || len(workspaceID) > 255 || strings.ContainsAny(workspaceID, "\r\n\x00") {
		return facts, nil
	}
	base, release, err := r.Client(ctx)
	if release != nil {
		defer release()
	}
	if err != nil || base == nil || base.Transport == nil || base.Timeout <= 0 {
		return facts, nil
	}
	client := *base
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	access.SessionID = uuid.NewString() // trusted, fresh per fact attempt
	read := selectedHTTPRead{client: &client, access: access, workspaceID: workspaceID}
	var activeUntil *time.Time
	var paid *int
	var memberList, inviteList []Member
	var subOK, countsOK, membersOK, invitesOK bool
	var denied bool
	activeUntil, paid, subOK = read.subscription(ctx, &facts)
	countsOK = read.counts(ctx, &facts)
	memberList, membersOK = read.members(ctx, &facts)
	inviteList, invitesOK = read.invites(ctx, &facts)
	for _, source := range facts.Sources {
		if source.Permission == "denied" {
			denied = true
		}
	}
	facts.Result.ObservedAt = time.Now().UTC()
	if denied {
		facts.Permission = "denied"
		facts.Result.Outcome = OutcomeForbidden
		return facts, nil
	}
	if !subOK || !countsOK || !membersOK || !invitesOK {
		facts.Result.Completeness = Partial
		return facts, nil
	}
	members, invites := len(memberList), len(inviteList)
	facts.Permission = "read"
	facts.Result = Result{Outcome: OutcomeOperational, Completeness: Complete, ObservedAt: facts.Result.ObservedAt,
		ActiveUntil: activeUntil, SeatLimit: paid, MemberCount: &members, PendingInviteCount: &invites, Members: append(memberList, inviteList...)}
	return facts, nil
}

type selectedHTTPRead struct {
	client      *http.Client
	access      WorkspaceAccess
	workspaceID string
}

// No arbitrary URL reaches the transport. These are the entire request set.
func (r selectedHTTPRead) get(ctx context.Context, endpoint string) ([]byte, Outcome) {
	var raw string
	encoded := url.PathEscape(r.workspaceID)
	switch endpoint {
	case "subscriptions":
		raw = selectedWorkspaceOrigin + "/backend-api/subscriptions?account_id=" + url.QueryEscape(r.workspaceID)
	case "seat_type_counts":
		raw = selectedWorkspaceOrigin + "/backend-api/accounts/" + encoded + "/users/seat_type_counts"
	default:
		return nil, OutcomeIncomplete
	}
	return r.request(ctx, raw, endpoint)
}

func (r selectedHTTPRead) page(ctx context.Context, endpoint string, offset int) ([]byte, Outcome) {
	if offset < 0 || offset >= selectedMaxEntries {
		return nil, OutcomeIncomplete
	}
	var raw string
	encoded := url.PathEscape(r.workspaceID)
	switch endpoint {
	case "workspace_members":
		raw = selectedWorkspaceOrigin + "/backend-api/accounts/" + encoded + "/users?offset=" + strconv.Itoa(offset) + "&limit=100&query="
	case "outbound_invites":
		raw = selectedWorkspaceOrigin + "/backend-api/accounts/" + encoded + "/invites?limit=100&offset=" + strconv.Itoa(offset)
	default:
		return nil, OutcomeIncomplete
	}
	return r.request(ctx, raw, endpoint)
}

func (r selectedHTTPRead) request(ctx context.Context, raw, endpoint string) ([]byte, Outcome) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil || req.URL.Scheme != "https" || req.URL.Host != "chatgpt.com" || req.URL.User != nil || req.URL.Fragment != "" || req.Method != http.MethodGet {
		return nil, OutcomeIncomplete
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.access.AccessToken)
	req.Header.Set("Origin", selectedWorkspaceOrigin)
	req.Header.Set("Referer", selectedWorkspaceOrigin+"/admin/members")
	req.Header.Set("User-Agent", browserAuthUA)
	req.Header.Set("Oai-Device-Id", r.access.DeviceID)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	if endpoint == "outbound_invites" {
		req.Header.Set("chatgpt-account-id", r.workspaceID)
		req.Header.Set("Oai-Session-Id", r.access.SessionID)
		req.Header.Set("Oai-Client-Build-Number", "9432397")
		req.Header.Set("Oai-Client-Version", "prod-1b777b39db92d7476d69bf3cd407f6b9a41f7410")
		req.Header.Set("Oai-Language", "zh-CN")
		req.Header.Set("X-Oai-Is-Client-Observation", "null")
		req.Header.Set("X-Oai-Is-Pending-Updates", `{"v":3,"updates":[]}`)
		req.Header.Set("X-OpenAI-Target-Path", "/backend-api/accounts/"+url.PathEscape(r.workspaceID)+"/invites")
		req.Header.Set("X-OpenAI-Target-Route", "/backend-api/accounts/{account_id}/invites")
	}
	for _, cookie := range r.access.Cookies {
		req.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
	}
	response, err := r.client.Do(req)
	if err != nil {
		return nil, Classify(0, "", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return nil, OutcomeUnauthorized
	}
	if response.StatusCode == http.StatusForbidden {
		return nil, OutcomeForbidden
	}
	if response.StatusCode != http.StatusOK {
		return nil, OutcomeIncomplete
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody+1))
	if err != nil || len(body) > maxResponseBody {
		return nil, OutcomeIncomplete
	}
	return body, OutcomeOperational
}

func appendReadSource(facts *SelectedWorkspaceFacts, endpoint string, observed time.Time, outcome Outcome) {
	completeness, permission := Unknown, "unknown"
	if outcome == OutcomeOperational {
		completeness, permission = Complete, "read"
	}
	if outcome == OutcomeUnauthorized || outcome == OutcomeForbidden {
		permission = "denied"
	}
	facts.Sources = append(facts.Sources, ReadEvidence{Source: endpoint, ObservedAt: observed, Outcome: outcome, Completeness: completeness, Permission: permission})
}

func (r selectedHTTPRead) subscription(ctx context.Context, facts *SelectedWorkspaceFacts) (*time.Time, *int, bool) {
	observed := time.Now().UTC()
	body, outcome := r.get(ctx, "subscriptions")
	var payload struct {
		ActiveUntil   string `json:"active_until"`
		SeatsInUse    *int   `json:"seats_in_use"`
		SeatsEntitled *int   `json:"seats_entitled"`
		SeatCapacity  []struct {
			Type string `json:"type"`
			Paid *int   `json:"paid"`
		} `json:"seat_capacity"`
	}
	var expiry *time.Time
	var paid *int
	if outcome == OutcomeOperational {
		if json.Unmarshal(body, &payload) != nil {
			outcome = OutcomeIncomplete
		} else {
			parsed, err := time.Parse(time.RFC3339, payload.ActiveUntil)
			if err == nil {
				utc := parsed.UTC()
				expiry = &utc
			}
			for _, item := range payload.SeatCapacity {
				if item.Type == "default" && paid == nil {
					paid = item.Paid
				}
			}
			if expiry == nil || paid == nil || *paid < 0 || payload.SeatsEntitled == nil || *payload.SeatsEntitled < 0 || payload.SeatsInUse == nil || *payload.SeatsInUse < 0 {
				outcome = OutcomeIncomplete
			}
		}
	}
	appendReadSource(facts, "subscriptions", observed, outcome)
	return expiry, paid, outcome == OutcomeOperational
}

func (r selectedHTTPRead) counts(ctx context.Context, facts *SelectedWorkspaceFacts) bool {
	observed := time.Now().UTC()
	body, outcome := r.get(ctx, "seat_type_counts")
	var payload struct {
		Counts struct {
			Default    *int `json:"default"`
			UsageBased *int `json:"usage_based"`
			Automation *int `json:"automation"`
			Prolite    *int `json:"prolite"`
		} `json:"seat_type_counts"`
	}
	if outcome == OutcomeOperational {
		if json.Unmarshal(body, &payload) != nil || payload.Counts.Default == nil || payload.Counts.UsageBased == nil || payload.Counts.Automation == nil || payload.Counts.Prolite == nil ||
			*payload.Counts.Default < 0 || *payload.Counts.UsageBased < 0 || *payload.Counts.Automation < 0 || *payload.Counts.Prolite < 0 {
			outcome = OutcomeIncomplete
		}
	}
	appendReadSource(facts, "seat_type_counts", observed, outcome)
	return outcome == OutcomeOperational
}

type selectedPage[T any] struct {
	Items  []T  `json:"items"`
	Total  *int `json:"total"`
	Limit  *int `json:"limit"`
	Offset *int `json:"offset"`
}

func fetchSelectedPages[T any](ctx context.Context, r selectedHTTPRead, endpoint string, convert func(T) (Member, bool)) ([]Member, Outcome) {
	var entries []Member
	seen := map[string]bool{}
	declared := -1
	for offset := 0; ; {
		body, outcome := r.page(ctx, endpoint, offset)
		if outcome != OutcomeOperational {
			return nil, outcome
		}
		var page selectedPage[T]
		if json.Unmarshal(body, &page) != nil || page.Total == nil || page.Offset == nil || page.Limit == nil || page.Items == nil || *page.Total < 0 || *page.Total > selectedMaxEntries || *page.Offset != offset || *page.Limit != selectedPageLimit || len(page.Items) > *page.Limit || len(page.Items) > *page.Total-offset {
			return nil, OutcomeIncomplete
		}
		if declared < 0 {
			declared = *page.Total
		} else if declared != *page.Total {
			return nil, OutcomeIncomplete
		}
		for _, raw := range page.Items {
			member, ok := convert(raw)
			if !ok || seen[member.Kind+":"+strings.ToLower(member.Identifier)] {
				return nil, OutcomeIncomplete
			}
			seen[member.Kind+":"+strings.ToLower(member.Identifier)] = true
			entries = append(entries, member)
		}
		offset += len(page.Items)
		if offset == declared {
			return entries, OutcomeOperational
		}
		if len(page.Items) == 0 || offset >= selectedMaxEntries {
			return nil, OutcomeIncomplete
		}
	}
}

func (r selectedHTTPRead) members(ctx context.Context, facts *SelectedWorkspaceFacts) ([]Member, bool) {
	observed := time.Now().UTC()
	type item struct {
		ID            string `json:"id"`
		AccountUserID string `json:"account_user_id"`
		Email         string `json:"email"`
		Role          string `json:"role"`
	}
	entries, outcome := fetchSelectedPages[item](ctx, r, "workspace_members", func(raw item) (Member, bool) {
		identifier := strings.ToLower(strings.TrimSpace(raw.Email))
		id := strings.TrimSpace(raw.ID)
		if id == "" {
			id = strings.TrimSpace(raw.AccountUserID)
		}
		return Member{Kind: "member", PlatformMemberID: id, Identifier: identifier, Status: "listed", Role: raw.Role}, id != "" && identifier != "" && len(identifier) <= 254 && len(raw.Role) <= 64
	})
	appendReadSource(facts, "workspace_members", observed, outcome)
	return entries, outcome == OutcomeOperational
}

func (r selectedHTTPRead) invites(ctx context.Context, facts *SelectedWorkspaceFacts) ([]Member, bool) {
	observed := time.Now().UTC()
	type item struct {
		EmailAddress string          `json:"email_address"`
		Email        string          `json:"email"`
		Status       json.RawMessage `json:"status"`
	}
	entries, outcome := fetchSelectedPages[item](ctx, r, "outbound_invites", func(raw item) (Member, bool) {
		identifier := strings.ToLower(strings.TrimSpace(raw.EmailAddress))
		if identifier == "" {
			identifier = strings.ToLower(strings.TrimSpace(raw.Email))
		}
		var status string
		if len(raw.Status) == 0 || json.Unmarshal(raw.Status, &status) != nil {
			status = strings.TrimSpace(string(raw.Status))
		}
		if status != "pending" && status != "2" {
			return Member{}, false
		}
		return Member{Kind: "pending_invite", Identifier: identifier, Status: "pending"}, identifier != "" && len(identifier) <= 254
	})
	appendReadSource(facts, "outbound_invites", observed, outcome)
	return entries, outcome == OutcomeOperational
}
