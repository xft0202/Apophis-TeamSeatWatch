package platform

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// These Web-client claims are from the reference Personal post-TOTP contract,
// not the separate Codex/Workspace OAuth client used for delivery.
const personalWebClientID = "app_X8zY6vW2pQ9tR3dE7nK1jL5gH"
const personalWebScopes = "openid email profile offline_access model.request model.read organization.read organization.write"

// Token claims are an admission hint, not signature verification. A fresh
// authenticated /api/auth/session response must supply this same token.
func postTOTPPersonalExpiry(token string, now time.Time) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(parts[1]) == 0 {
		return time.Time{}, errors.New("personal token is not a JWT")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(body) > 16384 {
		return time.Time{}, errors.New("personal JWT payload invalid")
	}
	var claims map[string]any
	if json.Unmarshal(body, &claims) != nil {
		return time.Time{}, errors.New("personal JWT claims invalid")
	}
	authClaims, _ := claims["https://api.openai.com/auth"].(map[string]any)
	mfaClaims, _ := claims["https://api.openai.com/mfa"].(map[string]any)
	clientID, err := consistentPersonalClaim(claims, authClaims, "client_id", false)
	if err != nil {
		return time.Time{}, err
	}
	accountID, err := consistentPersonalClaim(claims, authClaims, "chatgpt_account_id", false)
	if err != nil {
		return time.Time{}, err
	}
	plan, err := consistentPersonalClaim(claims, authClaims, "chatgpt_plan_type", true)
	if err != nil {
		return time.Time{}, err
	}
	// A Workspace-scoped flat claim overrides nested claims in the sourced
	// classifier. Reject disagreement instead of choosing either representation.
	if clientID != personalWebClientID || accountID == "" ||
		(plan != "free" && plan != "plus" && plan != "pro" && plan != "personal" && plan != "default") {
		return time.Time{}, errors.New("personal token scope invalid")
	}
	var scopeSet map[string]bool
	for _, candidate := range []any{claims["scp"], authClaims["scp"], claims["scope"], authClaims["scope"], claims["https://api.openai.com/auth.scp"], claims["https://api.openai.com/auth.scope"]} {
		if candidate == nil {
			continue
		}
		values := personalClaimStrings(candidate)
		if len(values) == 0 {
			return time.Time{}, errors.New("personal token scopes incomplete")
		}
		current := make(map[string]bool, len(values))
		for _, scope := range values {
			current[scope] = true
		}
		if scopeSet != nil && !samePersonalScopeSet(scopeSet, current) {
			return time.Time{}, errors.New("personal token scopes conflict")
		}
		scopeSet = current
	}
	for _, required := range strings.Fields(personalWebScopes) {
		if !scopeSet[required] {
			return time.Time{}, errors.New("personal token scopes incomplete")
		}
	}
	foundAMR := false
	for _, candidate := range []any{claims["amr"], authClaims["amr"], claims["https://api.openai.com/auth.amr"]} {
		if candidate == nil {
			continue
		}
		foundAMR = true
		foundTOTP := false
		for _, value := range personalClaimStrings(candidate) {
			if strings.EqualFold(value, "urn:openai:amr:otp_totp") {
				foundTOTP = true
			}
		}
		if !foundTOTP {
			return time.Time{}, errors.New("post-TOTP proof conflicts")
		}
	}
	foundMFA := false
	for _, candidate := range []any{mfaClaims["required"], claims["required"], claims["https://api.openai.com/mfa.required"]} {
		if candidate == nil {
			continue
		}
		foundMFA = true
		if candidate == true {
			continue
		}
		if value, ok := candidate.(string); ok && strings.EqualFold(strings.TrimSpace(value), "yes") {
			continue
		}
		return time.Time{}, errors.New("post-TOTP MFA proof conflicts")
	}
	if !foundAMR || !foundMFA {
		return time.Time{}, errors.New("post-TOTP proof missing")
	}
	exp, ok := claims["exp"].(float64)
	if !ok || exp <= 0 {
		return time.Time{}, errors.New("personal token expiration missing")
	}
	expires := time.Unix(int64(exp), 0).UTC()
	if !expires.After(now.Add(time.Minute)) {
		return time.Time{}, errors.New("personal token expiration invalid")
	}
	return expires, nil
}

func consistentPersonalClaim(raw, nested map[string]any, key string, foldCase bool) (string, error) {
	var result string
	for _, candidate := range []any{raw[key], nested[key], raw["https://api.openai.com/auth."+key]} {
		if candidate == nil {
			continue
		}
		value, ok := candidate.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return "", errors.New("personal token claim invalid")
		}
		value = strings.TrimSpace(value)
		if foldCase {
			value = strings.ToLower(value)
		}
		if result != "" && result != value {
			return "", errors.New("personal token claims conflict")
		}
		result = value
	}
	return result, nil
}

func samePersonalScopeSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for scope := range a {
		if !b[scope] {
			return false
		}
	}
	return true
}

func personalClaimStrings(value any) []string {
	var items []string
	switch typed := value.(type) {
	case string:
		items = strings.Fields(typed)
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				items = append(items, strings.TrimSpace(text))
			}
		}
	}
	return items
}
