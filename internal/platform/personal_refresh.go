package platform

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/browserenv"
)

type PersonalWebRefresher struct {
	Client  DiscoveryClient
	Browser PersonalBrowserFactory
}

// This local denial stays distinguishable through net/http's url.Error wrapper.
type personalRequestDenial string

func (e personalRequestDenial) Error() string { return string(e) }

func personalRequestFailure(err error) PersonalRefreshResult {
	failure := &PersonalRefreshFailure{Code: "login_request_failed"}
	var rejected *authHTTPError
	var network net.Error
	var request *url.Error
	var denied personalRequestDenial
	switch {
	case errors.As(err, &denied):
	// Keep the existing login_request_failed outcome for an intentional local refusal.
	case errors.As(err, &rejected):
		failure.HTTPStatus = rejected.status
		switch {
		case rejected.browserChallenge:
			failure.Code = "platform_browser_challenge"
		case rejected.status == http.StatusForbidden:
			failure.Code = "platform_access_denied"
		case rejected.status == http.StatusTooManyRequests:
			failure.Code = "platform_rate_limited"
		case rejected.status >= 500:
			failure.Code = "platform_unavailable"
		}
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network) && network.Timeout():
		failure.Code = "network_timeout"
	case errors.As(err, &request):
		failure.Code = "network_error"
	}
	return PersonalRefreshResult{Status: "refresh_failed", Failure: failure}
}

// Only the two first-party hosts are permitted, including every continuation
// returned by the remote service. The checked transport also guards shared
// helpers that resolve absolute redirect URLs without validating their host.
type personalHostGuard struct{ next http.RoundTripper }

func validatePersonalRequest(req *http.Request) error {
	if req == nil {
		return personalRequestDenial("personal login request missing")
	}
	u := req.URL
	if u == nil || u.Scheme != "https" || u.User != nil || u.Port() != "" ||
		(u.Hostname() != "chatgpt.com" && u.Hostname() != "auth.openai.com") ||
		(req.Host != "" && req.Host != u.Host) {
		return personalRequestDenial("personal login URL is not first-party HTTPS")
	}
	path := u.Path
	if personalChoiceRequest(req) {
		return nil
	}
	if authorization, _ := req.Context().Value(personalAuthorizationContextKey{}).(*personalAuthorization); authorization.allows(req.Method, u) {
		return nil
	}
	if strings.Contains(path, "/workspace/select") || strings.Contains(path, "/session/select") ||
		strings.Contains(path, "/consent") || strings.HasPrefix(path, "/oauth/") ||
		strings.HasPrefix(path, "/backend-api/") || strings.Contains(path, "/invites/") {
		return personalRequestDenial("personal login path is not permitted")
	}
	if req.Method == http.MethodPost {
		target := u.Hostname() + path
		if target != "chatgpt.com/api/auth/signin/openai" &&
			target != "auth.openai.com/api/accounts/password/verify" &&
			target != "auth.openai.com/api/accounts/mfa/verify" {
			return personalRequestDenial("personal login POST is not permitted")
		}
	} else if req.Method != http.MethodGet {
		return personalRequestDenial("personal login method is not permitted")
	}
	return nil
}

func (g personalHostGuard) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := validatePersonalRequest(req); err != nil {
		return nil, err
	}
	return g.next.RoundTrip(req)
}

func isCookieMaterial(password, totp string) bool {
	if auth.IsSessionMaterialMarker(totp) {
		return true
	}
	// If a valid TOTP secret is provided, only treat password as cookie material
	// when it carries an explicit session cookie name or JSON session envelope.
	// Never guess cookie mode from dot counts or length of a normal password.
	p := strings.TrimSpace(password)
	if strings.Contains(p, "__Secure-next-auth.session-token") {
		return true
	}
	if strings.HasPrefix(p, "{") && (strings.Contains(p, "\"sessionToken\"") || strings.Contains(p, "\"cookies\"")) {
		return true
	}
	return false
}

const nextAuthChunkSize = 3900

func appendSessionTokenCookie(cookies []SessionCookie, raw string) []SessionCookie {
	if raw == "" {
		return cookies
	}
	if len(raw) <= nextAuthChunkSize {
		return append(cookies, SessionCookie{Name: "__Secure-next-auth.session-token", Value: raw, Host: "chatgpt.com"})
	}
	for i, start := 0, 0; start < len(raw); i, start = i+1, start+nextAuthChunkSize {
		end := start + nextAuthChunkSize
		if end > len(raw) {
			end = len(raw)
		}
		cookies = append(cookies, SessionCookie{
			Name:  "__Secure-next-auth.session-token." + strconv.Itoa(i),
			Value: raw[start:end],
			Host:  "chatgpt.com",
		})
	}
	return cookies
}

// normalizeSuppliedCookies enforces canonical NextAuth chunking and rejects
// duplicate, conflicting, or mixed whole+chunked session cookies.
func normalizeSuppliedCookies(input []SessionCookie) ([]SessionCookie, bool) {
	const sessionName = "__Secure-next-auth.session-token"
	var out []SessionCookie
	seen := make(map[string]bool)
	hasWhole := false
	hasChunks := false
	for _, c := range input {
		host := strings.TrimSpace(c.Host)
		if host == "" {
			host = "chatgpt.com"
		}
		if host != "chatgpt.com" && host != "auth.openai.com" {
			return nil, false
		}
		name := strings.TrimSpace(c.Name)
		val := strings.TrimSpace(c.Value)
		if name == "" || val == "" || c.Name != name || len(name) > 128 || len(val) > 16384 || strings.ContainsAny(name+val, "\r\n	;\x00") {
			return nil, false
		}
		if (&http.Cookie{Name: name, Value: val}).Valid() != nil {
			return nil, false
		}
		key := host + "/" + name
		if seen[key] {
			return nil, false
		}
		seen[key] = true
		if host == "chatgpt.com" && name == sessionName {
			if hasWhole || hasChunks {
				return nil, false
			}
			hasWhole = true
			if len(val) > nextAuthChunkSize {
				out = appendSessionTokenCookie(out, val)
			} else {
				out = append(out, SessionCookie{Name: name, Value: val, Host: host})
			}
			continue
		}
		if host == "chatgpt.com" && strings.HasPrefix(name, sessionName+".") {
			if hasWhole {
				return nil, false
			}
			hasChunks = true
		}
		out = append(out, SessionCookie{Name: name, Value: val, Host: host})
	}
	if !hasBrowserSessionCookie(out) {
		return nil, false
	}
	return out, true
}

func validateSuppliedSessionEnvelope(accessToken, expiresRaw, userEmail, userID, expectedIdentifier string) bool {
	if strings.TrimSpace(expiresRaw) != "" {
		if _, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(expiresRaw)); err != nil {
			if _, err = time.Parse(time.RFC3339, strings.TrimSpace(expiresRaw)); err != nil {
				return false
			}
		}
	}
	// Do not reject a valid browser session cookie merely because a pasted JSON
	// snapshot contains an older/expired accessToken; remote /api/auth/session
	// is the authoritative source of token freshness and scope. Still verify
	// that any supplied snapshot identity claims are internally consistent and
	// match expectedIdentifier.
	if strings.TrimSpace(accessToken) != "" {
		parts := strings.Split(strings.TrimSpace(accessToken), ".")
		if len(parts) != 3 || len(parts[1]) == 0 {
			return false
		}
		body, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil || len(body) > 16384 {
			return false
		}
		var claims map[string]any
		if json.Unmarshal(body, &claims) != nil {
			return false
		}
		authClaims, _ := claims["https://api.openai.com/auth"].(map[string]any)
		profileClaims, _ := claims["https://api.openai.com/profile"].(map[string]any)
		email, err := consistentPersonalClaim(claims, profileClaims, "email", true)
		if err != nil {
			return false
		}
		userClaim, err := consistentPersonalClaim(claims, authClaims, "chatgpt_user_id", false)
		if err != nil {
			return false
		}
		subClaim, err := consistentPersonalClaim(claims, authClaims, "sub", false)
		if err != nil {
			return false
		}
		if strings.TrimSpace(userEmail) != "" && email != "" && !strings.EqualFold(strings.TrimSpace(userEmail), email) {
			return false
		}
		if strings.TrimSpace(userID) != "" && userClaim != "" && strings.TrimSpace(userID) != userClaim && strings.TrimSpace(userID) != subClaim {
			return false
		}
		if expectedIdentifier != "" {
			candidate := email
			if candidate == "" {
				candidate = strings.TrimSpace(userEmail)
			}
			if candidate != "" && !strings.EqualFold(candidate, strings.TrimSpace(expectedIdentifier)) {
				return false
			}
		}
	} else if strings.TrimSpace(userEmail) != "" && expectedIdentifier != "" {
		if !strings.EqualFold(strings.TrimSpace(userEmail), strings.TrimSpace(expectedIdentifier)) {
			return false
		}
	}
	return true
}

func parseSuppliedSessionMaterial(rawPassword, deviceID, expectedIdentifier string) (PersonalSession, bool) {
	raw := strings.TrimSpace(rawPassword)
	if raw == "" {
		return PersonalSession{}, false
	}
	var session PersonalSession
	session.DeviceID = deviceID
	// 1. Check if JSON envelope containing sessionToken or cookies ( plus optional /api/auth/session metadata )
	if strings.HasPrefix(raw, "{") {
		var snap struct {
			AccessToken  string          `json:"accessToken"`
			Expires      string          `json:"expires"`
			Cookies      []SessionCookie `json:"cookies"`
			SessionToken string          `json:"sessionToken"`
			User         struct {
				Email string `json:"email"`
				ID    string `json:"id"`
			} `json:"user"`
		}
		if json.Unmarshal([]byte(raw), &snap) != nil {
			return PersonalSession{}, false
		}
		var rawCookies []SessionCookie
		if len(snap.Cookies) > 0 {
			rawCookies = append(rawCookies, snap.Cookies...)
		}
		if strings.TrimSpace(snap.SessionToken) != "" {
			rawCookies = append(rawCookies, SessionCookie{Name: "__Secure-next-auth.session-token", Value: strings.TrimSpace(snap.SessionToken), Host: "chatgpt.com"})
		}
		// Never fabricate a session cookie from accessToken; verifiable browser cookies are required.
		normalized, ok := normalizeSuppliedCookies(rawCookies)
		if !ok {
			return PersonalSession{}, false
		}
		if !validateSuppliedSessionEnvelope(snap.AccessToken, snap.Expires, snap.User.Email, snap.User.ID, expectedIdentifier) {
			return PersonalSession{}, false
		}
		session.Cookies = normalized
		return session, validPersonalCookies(session)
	}
	// 2. Check if semicolon-separated cookie header or key=value pairs (e.g. Cookie: a=b; c=d)
	cleaned := raw
	if strings.HasPrefix(strings.ToLower(cleaned), "cookie:") {
		cleaned = strings.TrimSpace(cleaned[len("cookie:"):])
	}
	if strings.Contains(cleaned, "=") || strings.Contains(cleaned, ";") {
		var rawCookies []SessionCookie
		for _, part := range strings.Split(cleaned, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			eq := strings.IndexByte(part, '=')
			if eq <= 0 {
				return PersonalSession{}, false
			}
			name := strings.TrimSpace(part[:eq])
			val := strings.TrimSpace(part[eq+1:])
			if name == "__Secure-next-auth.session-token" || strings.HasPrefix(name, "__Secure-next-auth.session-token.") || name == "_puid" || name == "oai-sc" || name == "_account" || name == "oai-workspace" {
				rawCookies = append(rawCookies, SessionCookie{Name: name, Value: val, Host: "chatgpt.com"})
			}
		}
		normalized, ok := normalizeSuppliedCookies(rawCookies)
		if !ok {
			return PersonalSession{}, false
		}
		session.Cookies = normalized
		return session, validPersonalCookies(session)
	}
	// 3. Reject bare 3-segment JWT accessTokens (a valid NextAuth JWE session token has 5 segments or opaque token format)
	if strings.Count(raw, ".") == 2 && strings.HasPrefix(raw, "eyJ") {
		return PersonalSession{}, false
	}
	// 4. Otherwise treat raw string as __Secure-next-auth.session-token value directly
	if !strings.ContainsAny(raw, " 	\r\n;") {
		normalized, ok := normalizeSuppliedCookies([]SessionCookie{{Name: "__Secure-next-auth.session-token", Value: raw, Host: "chatgpt.com"}})
		if !ok {
			return PersonalSession{}, false
		}
		session.Cookies = normalized
	}
	return session, validPersonalCookies(session)
}

func isSavedFederatedSession(saved PersonalSession) bool {
	parts := strings.Split(strings.TrimSpace(saved.AccessToken), ".")
	if len(parts) != 3 || len(parts[1]) == 0 {
		return false
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(body) > 16384 {
		return false
	}
	var claims map[string]any
	if json.Unmarshal(body, &claims) != nil {
		return false
	}
	authClaims, _ := claims["https://api.openai.com/auth"].(map[string]any)
	sub, err := consistentPersonalClaim(claims, authClaims, "sub", false)
	return err == nil && isFederatedOAuthSubject(sub)
}

// RefreshPersonal authenticates a fresh Personal-only session. Its first-party
// Web authorization never grants a Workspace or delivery OAuth capability.
// A multi-account login may select its independently identified Personal account.
func (p PersonalWebRefresher) RefreshPersonal(ctx context.Context, material MotherMaterial) (PersonalRefreshResult, error) {
	failed := PersonalRefreshResult{Status: "refresh_failed"}
	if err := material.Environment.Validate(); err != nil {
		return failed, err
	}
	if p.Client == nil || strings.TrimSpace(material.LoginIdentifier) == "" || material.Password == "" || material.TOTPSecret == "" {
		return failed, nil
	}
	if isCookieMaterial(material.Password, material.TOTPSecret) {
		supplied, ok := parseSuppliedSessionMaterial(material.Password, material.Environment.DeviceID, material.LoginIdentifier)
		if !ok {
			return PersonalRefreshResult{Status: "invalid_login"}, nil
		}
		return p.renewPersonalWithIdentifier(ctx, supplied, material.Environment, material.LoginIdentifier, material.AllowFederatedWorkspace)
	}
	if _, err := auth.TOTPCode(material.TOTPSecret, time.Now()); err != nil {
		return failed, nil
	}
	source, release, err := p.Client(ctx)
	if err != nil {
		return personalRequestFailure(err), nil
	}
	if release != nil {
		defer release()
	}
	// Admission waiting uses only caller cancellation; execution starts here.
	ctx, stopExecution := context.WithTimeout(ctx, 120*time.Second)
	defer stopExecution()
	if source == nil || source.Transport == nil || source.Timeout <= 0 {
		return failed, nil
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return failed, nil
	}
	factory := p.Browser
	if factory == nil {
		factory = NewPersonalBrowser
	}
	transport, closeBrowser, err := factory(ctx, source, jar, material.Environment)
	if err != nil {
		return personalRequestFailure(err), nil
	}
	defer closeBrowser()
	client := *source
	if client.Timeout < 35*time.Second {
		client.Timeout = 35 * time.Second
	}
	client.Transport = personalHostGuard{next: transport}
	client.Jar = jar
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	r := &HTTPReader{client: &client}
	deviceID := material.Environment.DeviceID
	loggingID, err := newBrowserID()
	if err != nil {
		return failed, nil
	}
	loginURL, err := r.beginAccountLogin(ctx, material.LoginIdentifier, deviceID, loggingID)
	if err != nil {
		return personalRequestFailure(err), nil
	}
	authorization, err := personalAuthorizationFromLoginURL(loginURL)
	if err != nil {
		return personalRequestFailure(err), nil
	}
	if authorization != nil {
		ctx = context.WithValue(ctx, personalAuthorizationContextKey{}, authorization)
	}
	pageURL, page, err := r.followBrowserRedirect(ctx, jar, loginURL, officialChatBase+"/login")
	if err != nil {
		return personalRequestFailure(err), nil
	}
	if authorization.completedAt(pageURL) {
		return r.readPersonalSession(ctx, jar, deviceID, false, material.LoginIdentifier, false)
	}
	if !strings.Contains(strings.ToLower(pageURL+" "+string(page)), "log-in/password") {
		pageURL, page, err = r.followBrowserRedirect(ctx, jar, officialAuthBase+"/log-in/password", pageURL)
		if err != nil {
			return personalRequestFailure(err), nil
		}
		if !strings.Contains(strings.ToLower(pageURL+" "+string(page)), "log-in/password") {
			return failed, nil
		}
	}
	continueURL, err := r.submitPassword(ctx, jar, material.Password)
	if err != nil {
		var rejected *authHTTPError
		if errors.As(err, &rejected) && rejected.status == http.StatusUnauthorized {
			return PersonalRefreshResult{Status: "invalid_login"}, nil
		}
		return personalRequestFailure(err), nil
	}
	if continueURL == "" {
		return failed, nil
	}

	continuation, err := http.NewRequestWithContext(ctx, http.MethodGet, continueURL, nil)
	if err != nil || validatePersonalRequest(continuation) != nil {
		return failed, nil
	}
	challenge := extractMFAChallenge(continueURL, nil)
	pageURL = continueURL
	// The password response normally identifies the TOTP challenge directly.
	// Only read the continuation when it does not yet contain that identifier.
	if challenge == "" {
		pageURL, page, err = r.followBrowserRedirect(ctx, jar, continueURL, officialAuthBase+"/log-in/password")
		if err != nil {
			return personalRequestFailure(err), nil
		}
		if !requiresTOTP(pageURL, page) {
			return failed, nil
		}
		challenge = extractMFAChallenge(pageURL, page)
	}
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
		return personalRequestFailure(err), nil
	}
	if next := continueURLFromJSON(verified); next != "" {
		if pageURL, _, err = r.followBrowserRedirect(ctx, jar, next, pageURL); err != nil {
			return personalRequestFailure(err), nil
		}
	}
	if err = r.finishPersonalChoice(ctx, jar, pageURL); err != nil {
		return personalRequestFailure(err), nil
	}
	return r.readPersonalSession(ctx, jar, deviceID, false, "", false)
}

// RenewPersonal checks the saved session on the same fixed first-party endpoint.
// Direct RenewPersonal enforces strict Personal-only token boundaries; mother
// workspace discovery uses MotherMaterial{AllowFederatedWorkspace: true} via AcquirePersonal.
func (p PersonalWebRefresher) RenewPersonal(ctx context.Context, saved PersonalSession, environment browserenv.Environment) (PersonalRefreshResult, error) {
	return p.renewPersonalWithIdentifier(ctx, saved, environment, "", false)
}

func (p PersonalWebRefresher) renewPersonalWithIdentifier(ctx context.Context, saved PersonalSession, environment browserenv.Environment, expectedIdentifier string, allowFederatedWorkspace bool) (PersonalRefreshResult, error) {
	failed := PersonalRefreshResult{Status: "refresh_failed"}
	if err := environment.Validate(); err != nil {
		return failed, err
	}
	if p.Client == nil || !validPersonalCookies(saved) {
		return failed, nil
	}
	source, release, err := p.Client(ctx)
	if err != nil {
		return personalRequestFailure(err), nil
	}
	if release != nil {
		defer release()
	}
	ctx, stopExecution := context.WithTimeout(ctx, 120*time.Second)
	defer stopExecution()
	if source == nil || source.Transport == nil || source.Timeout <= 0 {
		return failed, nil
	}
	saved.DeviceID = environment.DeviceID
	jar, err := personalCookieJar(saved)
	if err != nil {
		return failed, nil
	}
	factory := p.Browser
	if factory == nil {
		factory = NewPersonalBrowser
	}
	transport, closeBrowser, err := factory(ctx, source, jar, environment)
	if err != nil {
		return personalRequestFailure(err), nil
	}
	defer closeBrowser()
	client := *source
	if client.Timeout < 35*time.Second {
		client.Timeout = 35 * time.Second
	}
	client.Transport = personalHostGuard{next: transport}
	client.Jar = jar
	return (&HTTPReader{client: &client}).readPersonalSession(ctx, jar, environment.DeviceID, true, expectedIdentifier, allowFederatedWorkspace)
}

func personalCookieJar(saved PersonalSession) (http.CookieJar, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	for _, cookie := range saved.Cookies {
		host := cookie.Host
		if host == "" {
			host = "chatgpt.com"
		}
		u, _ := url.Parse("https://" + host + "/")
		jar.SetCookies(u, []*http.Cookie{{Name: cookie.Name, Value: cookie.Value, Path: "/", Secure: true}})
	}
	u, _ := url.Parse(officialChatBase + "/")
	jar.SetCookies(u, []*http.Cookie{{Name: "oai-did", Value: saved.DeviceID, Path: "/", Secure: true}})
	return jar, nil
}

func (r *HTTPReader) readPersonalSession(ctx context.Context, jar http.CookieJar, deviceID string, renewal bool, expectedIdentifier string, allowFederatedWorkspace bool) (PersonalRefreshResult, error) {
	failed := PersonalRefreshResult{Status: "refresh_failed"}
	sessionBody, err := r.authJSONWithJar(ctx, jar, http.MethodGet, officialChatBase+"/api/auth/session", nil, officialChatBase+"/")
	if err != nil {
		var rejected *authHTTPError
		if renewal && errors.As(err, &rejected) && rejected.status == http.StatusUnauthorized {
			return PersonalRefreshResult{Status: "invalid_login"}, nil
		}
		return personalRequestFailure(err), nil
	}
	var snapshot struct {
		AccessToken string `json:"accessToken"`
		Expires     string `json:"expires"`
		User        struct {
			Email string `json:"email"`
			ID    string `json:"id"`
		} `json:"user"`
	}
	if json.Unmarshal(sessionBody, &snapshot) != nil {
		return failed, nil
	}
	if snapshot.AccessToken == "" {
		// Only an explicit empty session object proves the user is logged out.
		var fields map[string]json.RawMessage
		if renewal && json.Unmarshal(sessionBody, &fields) == nil && fields != nil && len(fields) == 0 {
			return PersonalRefreshResult{Status: "invalid_login"}, nil
		}
		return failed, nil
	}
	now := time.Now().UTC()
	tokenExpiry, err := personalTokenExpiry(snapshot.AccessToken, now, allowFederatedWorkspace)
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
	stored := make([]SessionCookie, 0)
	for _, host := range []string{"chatgpt.com", "auth.openai.com"} {
		u, _ := url.Parse("https://" + host + "/")
		for _, cookie := range jar.Cookies(u) {
			stored = append(stored, SessionCookie{Name: cookie.Name, Value: cookie.Value, Host: host})
		}
	}
	result := PersonalRefreshResult{Status: "ready", Session: PersonalSession{AccessToken: snapshot.AccessToken, Cookies: stored, DeviceID: deviceID, ExpiresAt: tokenExpiry, SessionExpiresAt: sessionExpiry}}
	if !ValidatePersonalRefresh(result, now) {
		return failed, nil
	}
	if expectedIdentifier != "" {
		identity, err := personalBearerIdentityWithPolicy(result.Session, snapshot.User.Email, snapshot.User.ID, now, allowFederatedWorkspace)
		if err != nil || !strings.EqualFold(identity.Identifier, strings.TrimSpace(expectedIdentifier)) {
			return failed, nil
		}
	}
	return result, nil
}
