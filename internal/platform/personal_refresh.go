package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
)

type PersonalWebRefresher struct{ Client DiscoveryClient }

// Only the two first-party hosts are permitted, including every continuation
// returned by the remote service. The checked transport also guards shared
// helpers that resolve absolute redirect URLs without validating their host.
type personalHostGuard struct{ next http.RoundTripper }

func (g personalHostGuard) RoundTrip(req *http.Request) (*http.Response, error) {
	u := req.URL
	if u == nil || u.Scheme != "https" || u.User != nil || u.Port() != "" ||
		(u.Hostname() != "chatgpt.com" && u.Hostname() != "auth.openai.com") ||
		(req.Host != "" && req.Host != u.Host) {
		return nil, errors.New("Personal login URL is not first-party HTTPS")
	}
	path := u.Path
	if strings.Contains(path, "/workspace/select") || strings.Contains(path, "/session/select") ||
		strings.Contains(path, "/consent") || strings.HasPrefix(path, "/oauth/") ||
		strings.HasPrefix(path, "/backend-api/") || strings.Contains(path, "/invites/") {
		return nil, errors.New("Personal login path is not permitted")
	}
	if req.Method == http.MethodPost {
		target := u.Hostname() + path
		if target != "chatgpt.com/api/auth/signin/openai" &&
			target != "auth.openai.com/api/accounts/password/verify" &&
			target != "auth.openai.com/api/accounts/mfa/verify" {
			return nil, errors.New("Personal login POST is not permitted")
		}
	} else if req.Method != http.MethodGet {
		return nil, errors.New("Personal login method is not permitted")
	}
	return g.next.RoundTrip(req)
}

// RefreshPersonal authenticates a fresh Personal-only session. It never calls
// workspace/select, consent, PKCE, account invite, or any Workspace action.
func (p PersonalWebRefresher) RefreshPersonal(ctx context.Context, material MotherMaterial) (PersonalRefreshResult, error) {
	failed := PersonalRefreshResult{Status: "refresh_failed"}
	if p.Client == nil || strings.TrimSpace(material.LoginIdentifier) == "" || material.Password == "" || material.TOTPSecret == "" {
		return failed, nil
	}
	if _, err := auth.TOTPCode(material.TOTPSecret, time.Now()); err != nil {
		return failed, nil
	}
	source, release, err := p.Client(ctx)
	if err != nil {
		return failed, nil
	}
	if release != nil {
		defer release()
	}
	if source == nil || source.Transport == nil || source.Timeout <= 0 {
		return failed, nil
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return failed, nil
	}
	client := *source
	client.Transport = personalHostGuard{next: source.Transport}
	client.Jar = jar
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	r := &HTTPReader{client: &client}
	deviceID, err := newBrowserID()
	if err != nil {
		return failed, nil
	}
	loggingID, err := newBrowserID()
	if err != nil {
		return failed, nil
	}
	loginURL, err := r.beginAccountLogin(ctx, material.LoginIdentifier, deviceID, loggingID)
	if err != nil {
		return failed, nil
	}
	pageURL, page, err := r.followBrowserRedirect(ctx, jar, loginURL, officialChatBase+"/login")
	if err != nil {
		return failed, nil
	}
	if !strings.Contains(strings.ToLower(pageURL+" "+string(page)), "log-in/password") {
		pageURL, page, err = r.followBrowserRedirect(ctx, jar, officialAuthBase+"/log-in/password", pageURL)
		if err != nil || !strings.Contains(strings.ToLower(pageURL+" "+string(page)), "log-in/password") {
			return failed, nil
		}
	}
	continueURL, err := r.submitPassword(ctx, jar, material.Password)
	if err != nil {
		var rejected *authHTTPError
		if errors.As(err, &rejected) && rejected.status == http.StatusUnauthorized {
			return PersonalRefreshResult{Status: "invalid_login"}, nil
		}
		return failed, nil
	}
	if continueURL == "" {
		return failed, nil
	}
	pageURL, page, err = r.followBrowserRedirect(ctx, jar, continueURL, officialAuthBase+"/log-in/password")
	if err != nil || !requiresTOTP(pageURL, page) {
		return failed, nil
	}
	challenge := extractMFAChallenge(pageURL, page)
	if challenge == "" {
		return failed, nil
	}
	code, err := auth.TOTPCode(material.TOTPSecret, time.Now())
	if err != nil {
		return failed, nil
	}
	body, _ := json.Marshal(map[string]string{"code": code, "type": "totp", "id": challenge})
	verified, err := r.authJSONWithJar(ctx, jar, http.MethodPost, officialAuthBase+"/api/accounts/mfa/verify", body, pageURL)
	if err != nil {
		var rejected *authHTTPError
		if errors.As(err, &rejected) && rejected.status == http.StatusUnauthorized {
			return PersonalRefreshResult{Status: "invalid_login"}, nil
		}
		return failed, nil
	}
	if next := continueURLFromJSON(verified); next != "" {
		if _, _, err = r.followBrowserRedirect(ctx, jar, next, pageURL); err != nil {
			return failed, nil
		}
	}
	// Fresh session GET is the post-TOTP confirmation. Never accept a password-
	// only page or a Workspace token just because the MFA endpoint returned 2xx.
	sessionBody, err := r.authJSONWithJar(ctx, jar, http.MethodGet, officialChatBase+"/api/auth/session", nil, officialChatBase+"/")
	if err != nil {
		return failed, nil
	}
	var snapshot struct {
		AccessToken string `json:"accessToken"`
		Expires     string `json:"expires"`
	}
	if json.Unmarshal(sessionBody, &snapshot) != nil || snapshot.AccessToken == "" {
		return failed, nil
	}
	now := time.Now().UTC()
	tokenExpiry, err := postTOTPPersonalExpiry(snapshot.AccessToken, now)
	if err != nil {
		return failed, nil
	}
	sessionExpiry, err := time.Parse(time.RFC3339, snapshot.Expires)
	if err != nil || !sessionExpiry.After(now.Add(time.Minute)) {
		return failed, nil
	}
	if sessionExpiry.Before(tokenExpiry) {
		tokenExpiry = sessionExpiry
	}
	chatURL, _ := url.Parse(officialChatBase + "/")
	stored := make([]SessionCookie, 0)
	for _, cookie := range jar.Cookies(chatURL) {
		stored = append(stored, SessionCookie{Name: cookie.Name, Value: cookie.Value})
	}
	result := PersonalRefreshResult{Status: "ready", Session: PersonalSession{AccessToken: snapshot.AccessToken, Cookies: stored, DeviceID: deviceID, ExpiresAt: tokenExpiry}}
	if !ValidatePersonalRefresh(result, now) {
		return failed, nil
	}
	return result, nil
}
