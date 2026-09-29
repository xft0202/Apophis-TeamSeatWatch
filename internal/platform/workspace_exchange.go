package platform

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// OfficialWorkspaceTokenExchanger is an explicit Owner action only. Although
// the sourced operation is a GET, it changes the isolated browser session's
// current account. It must never run from discovery or a facts read.
type OfficialWorkspaceTokenExchanger struct{ Client DiscoveryClient }

var ErrWorkspaceExchangeUnavailable = errors.New("workspace token exchange unavailable")

const workspaceExchangeBodyLimit = 64 << 10

type workspaceExchangeGuard struct {
	next  http.RoundTripper
	query string
}

func (g workspaceExchangeGuard) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet || req.URL == nil || req.URL.Scheme != "https" || req.URL.Host != "chatgpt.com" ||
		req.URL.User != nil || req.URL.Fragment != "" || req.URL.Path != "/api/auth/session" ||
		req.URL.RawQuery != g.query || (req.Host != "" && req.Host != req.URL.Host) {
		return nil, ErrWorkspaceExchangeUnavailable
	}
	return g.next.RoundTrip(req)
}

func (e OfficialWorkspaceTokenExchanger) ExchangeWorkspace(ctx context.Context, personal PersonalSession, workspaceID string) (WorkspaceAccess, error) {
	failed := WorkspaceAccess{}
	if e.Client == nil || !ValidatePersonalRefresh(PersonalRefreshResult{Status: "ready", Session: personal}, time.Now()) ||
		workspaceID == "" || len(workspaceID) > 255 || strings.TrimSpace(workspaceID) != workspaceID || strings.ContainsAny(workspaceID, "\r\n\x00") {
		return failed, ErrWorkspaceExchangeUnavailable
	}
	base, release, err := e.Client(ctx)
	if release != nil {
		defer release()
	}
	if err != nil || base == nil || base.Transport == nil || base.Timeout <= 0 {
		return failed, ErrWorkspaceExchangeUnavailable
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return failed, ErrWorkspaceExchangeUnavailable
	}
	origin, _ := url.Parse(selectedWorkspaceOrigin + "/")
	cookies := make([]*http.Cookie, 0, len(personal.Cookies))
	for _, cookie := range personal.Cookies {
		cookies = append(cookies, &http.Cookie{Name: cookie.Name, Value: cookie.Value, Path: "/", Secure: true})
	}
	jar.SetCookies(origin, cookies)
	// The reference exchange sets these account cookies before its session GET.
	// The jar is per-attempt; no saved Personal cookie is changed.
	jar.SetCookies(origin, []*http.Cookie{
		{Name: "_account", Value: workspaceID, Path: "/", Secure: true},
		{Name: "_account_residency_region", Path: "/", MaxAge: -1},
		{Name: "_account_routing_override", Path: "/", MaxAge: -1},
		{Name: "_account_is_fedramp", Value: "false", Path: "/", Secure: true},
		{Name: "oai-workspace", Value: workspaceID, Path: "/", Secure: true},
		{Name: "oai-workspace-residency-region", Path: "/", MaxAge: -1},
		{Name: "oai-workspace-routing-override", Path: "/", MaxAge: -1},
	})
	query := "exchange_workspace_token=true&workspace_id=" + url.QueryEscape(workspaceID) + "&reason=setCurrentAccountWithoutRedirect"
	client := *base
	client.Transport = workspaceExchangeGuard{next: base.Transport, query: query}
	client.Jar = jar
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, selectedWorkspaceOrigin+"/api/auth/session?"+query, nil)
	if err != nil {
		return failed, ErrWorkspaceExchangeUnavailable
	}
	req.Header.Set("User-Agent", browserAuthUA)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Oai-Device-Id", personal.DeviceID)
	req.Header.Set("Oai-Client-Version", "prod-75c89a339d12aa428439cc667726d9e2351c5334")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("X-Openai-Target-Path", "/api/auth/session")
	req.Header.Set("X-Openai-Target-Route", "/api/auth/session")
	req.Header.Set("Referer", selectedWorkspaceOrigin+"/")
	response, err := client.Do(req)
	if err != nil {
		return failed, ErrWorkspaceExchangeUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return failed, ErrWorkspaceExchangeUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, workspaceExchangeBodyLimit+1))
	if err != nil || len(body) > workspaceExchangeBodyLimit {
		return failed, ErrWorkspaceExchangeUnavailable
	}
	var snapshot struct {
		AccessToken string `json:"accessToken"`
		Expires     string `json:"expires"`
		Account     struct {
			ID string `json:"id"`
		} `json:"account"`
	}
	if json.Unmarshal(body, &snapshot) != nil || snapshot.Account.ID != workspaceID {
		return failed, ErrWorkspaceExchangeUnavailable
	}
	sessionExpiry, err := time.Parse(time.RFC3339, snapshot.Expires)
	if err != nil {
		return failed, ErrWorkspaceExchangeUnavailable
	}
	now := time.Now().UTC()
	jwtExpiry, err := workspaceJWTExpiry(snapshot.AccessToken, workspaceID, now)
	if err != nil {
		return failed, ErrWorkspaceExchangeUnavailable
	}
	expires := jwtExpiry
	if sessionExpiry.Before(expires) {
		expires = sessionExpiry
	}
	if personal.ExpiresAt.Before(expires) {
		expires = personal.ExpiresAt
	}
	access := WorkspaceAccess{AccessToken: snapshot.AccessToken, WorkspaceID: workspaceID, DeviceID: personal.DeviceID, SessionID: uuid.NewString(), ExpiresAt: expires}
	for _, cookie := range jar.Cookies(origin) {
		access.Cookies = append(access.Cookies, SessionCookie{Name: cookie.Name, Value: cookie.Value})
	}
	if !ValidateWorkspaceAccess(access, workspaceID, now) {
		return failed, ErrWorkspaceExchangeUnavailable
	}
	return access, nil
}

// JWT claims here are an admission hint, not independent signature proof: the
// token must also come from the fixed authenticated HTTPS session response.
func workspaceJWTExpiry(token, workspaceID string, now time.Time) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(token) > 16384 || len(parts[1]) == 0 {
		return time.Time{}, ErrWorkspaceExchangeUnavailable
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(payload) > 16384 {
		return time.Time{}, ErrWorkspaceExchangeUnavailable
	}
	var claims struct {
		Expires   json.Number `json:"exp"`
		AccountID string      `json:"chatgpt_account_id"`
		FlatID    string      `json:"https://api.openai.com/auth.chatgpt_account_id"`
		Auth      struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return time.Time{}, ErrWorkspaceExchangeUnavailable
	}
	found := false
	for _, actual := range []string{claims.AccountID, claims.FlatID, claims.Auth.AccountID} {
		if actual == "" {
			continue
		}
		found = true
		if actual != workspaceID {
			return time.Time{}, ErrWorkspaceExchangeUnavailable
		}
	}
	seconds, err := claims.Expires.Int64()
	if !found || err != nil {
		return time.Time{}, ErrWorkspaceExchangeUnavailable
	}
	expiry := time.Unix(seconds, 0).UTC()
	if !expiry.After(now.Add(time.Minute)) || expiry.After(now.Add(24*time.Hour)) {
		return time.Time{}, ErrWorkspaceExchangeUnavailable
	}
	return expiry, nil
}

func ValidateWorkspaceAccess(access WorkspaceAccess, workspaceID string, now time.Time) bool {
	if access.WorkspaceID != workspaceID || access.DeviceID == "" || len(access.DeviceID) > 128 ||
		strings.ContainsAny(access.DeviceID, "\r\n\x00") || len(access.Cookies) == 0 || len(access.Cookies) > 50 ||
		access.ExpiresAt.Before(now.Add(30*time.Second)) || access.ExpiresAt.After(now.Add(24*time.Hour)) {
		return false
	}
	if _, err := uuid.Parse(access.SessionID); err != nil {
		return false
	}
	jwtExpiry, err := workspaceJWTExpiry(access.AccessToken, workspaceID, now)
	if err != nil || access.ExpiresAt.After(jwtExpiry) {
		return false
	}
	var savedSession, accountCookie, workspaceCookie bool
	for _, cookie := range access.Cookies {
		if cookie.Name == "" || len(cookie.Name) > 128 || cookie.Value == "" || len(cookie.Value) > 16384 || strings.ContainsAny(cookie.Name+cookie.Value, "\r\n\t;\x00") {
			return false
		}
		switch cookie.Name {
		case "__Secure-next-auth.session-token":
			savedSession = true
		case "_account":
			accountCookie = cookie.Value == workspaceID
		case "oai-workspace":
			workspaceCookie = cookie.Value == workspaceID
		}
	}
	return savedSession && accountCookie && workspaceCookie
}
