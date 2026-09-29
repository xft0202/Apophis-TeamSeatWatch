package platform

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"time"
)

const accountsCheckURL = "https://chatgpt.com/backend-api/accounts/check/v4-2023-04-27?timezone_offset_min=-480"

// DiscoveryClient leases one already-admitted transport for this read attempt.
// A production caller must supply an egress lease, not http.DefaultClient.
type DiscoveryClient func(context.Context) (*http.Client, func(), error)

type AccountsCheckDiscovery struct{ Client DiscoveryClient }

func (a AccountsCheckDiscovery) Discover(ctx context.Context, session PersonalSession) (DiscoveryResult, error) {
	if a.Client == nil || !ValidatePersonalRefresh(PersonalRefreshResult{Status: "ready", Session: session}, time.Now()) {
		return DiscoveryResult{Status: "discovery_failed"}, nil
	}
	source, release, err := a.Client(ctx)
	if err != nil {
		return DiscoveryResult{Status: "discovery_failed"}, nil
	}
	if release != nil {
		defer release()
	}
	if source == nil || source.Transport == nil || source.Timeout <= 0 {
		return DiscoveryResult{Status: "discovery_failed"}, nil
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return DiscoveryResult{}, err
	}
	target, _ := url.Parse("https://chatgpt.com/")
	cookies := make([]*http.Cookie, 0, len(session.Cookies))
	for _, cookie := range session.Cookies {
		cookies = append(cookies, &http.Cookie{Name: cookie.Name, Value: cookie.Value, Path: "/", Secure: true})
	}
	jar.SetCookies(target, cookies)
	client := *source
	client.Jar = jar
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, accountsCheckURL, nil)
	if err != nil {
		return DiscoveryResult{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+session.AccessToken)
	req.Header.Set("oai-device-id", session.DeviceID)
	req.Header.Set("User-Agent", browserAuthUA)
	resp, err := client.Do(req)
	if err != nil {
		return DiscoveryResult{Status: "discovery_failed"}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return DiscoveryResult{Status: "session_expired"}, nil
	}
	if resp.StatusCode == http.StatusForbidden {
		return DiscoveryResult{Status: "permission_denied"}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return DiscoveryResult{Status: "discovery_failed"}, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil || len(body) > maxResponseBody {
		return DiscoveryResult{Status: "discovery_failed"}, nil
	}
	workspaces, err := ParseAccountsCheckWorkspaces(body)
	if err != nil {
		return DiscoveryResult{Status: "discovery_failed"}, nil
	}
	return DiscoveryResult{Status: "discovered", Workspaces: workspaces}, nil
}

// ParseAccountsCheckWorkspaces accepts the sourced accounts map, not HTML,
// invitation candidates or a client-provided workspace ID. Missing facts fail closed.
func ParseAccountsCheckWorkspaces(data []byte) ([]DiscoveredWorkspace, error) {
	var payload struct {
		Accounts map[string]struct {
			Account *struct {
				ID        string `json:"account_id"`
				Name      string `json:"name"`
				Plan      string `json:"plan_type"`
				Structure string `json:"structure"`
				Kind      string `json:"kind"`
			} `json:"account"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || payload.Accounts == nil {
		return nil, errors.New("accounts check response missing accounts map")
	}
	if len(payload.Accounts) > 1000 {
		return nil, errors.New("accounts check response too large")
	}
	keys := make([]string, 0, len(payload.Accounts))
	for key := range payload.Accounts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	workspaces := make([]DiscoveredWorkspace, 0, len(keys))
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		item := payload.Accounts[key].Account
		if item == nil {
			return nil, errors.New("accounts check account missing")
		}
		id, name := strings.TrimSpace(item.ID), strings.TrimSpace(item.Name)
		plan, kind := strings.ToLower(strings.TrimSpace(item.Plan)), strings.ToLower(strings.TrimSpace(item.Kind))
		structure := strings.ToLower(strings.TrimSpace(item.Structure))
		isExcluded := func(value string) bool { return value == "personal" || value == "free" || value == "default" }
		if isExcluded(strings.ToLower(strings.TrimSpace(key))) || isExcluded(strings.ToLower(id)) ||
			isExcluded(plan) || isExcluded(kind) || isExcluded(structure) {
			continue
		}
		// Team/Business are the only evidenced target plans. Unknown plans or
		// kinds cannot become selectable merely because structure says workspace.
		if structure != "workspace" || (plan != "team" && plan != "business") ||
			(kind != "" && kind != "workspace" && kind != "team") {
			return nil, errors.New("accounts check Team plan or kind unknown")
		}
		if id == "" || name == "" || len(id) > 255 || len(name) > 120 {
			return nil, errors.New("accounts check workspace identity incomplete")
		}
		if !seen[id] {
			workspaces = append(workspaces, DiscoveredWorkspace{PlatformID: id, Name: name, Access: "readable"})
			seen[id] = true
		}
	}
	return workspaces, nil
}
