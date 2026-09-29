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
		return time.Time{}, errors.New("Personal token is not a JWT")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(body) > 16384 {
		return time.Time{}, errors.New("Personal JWT payload invalid")
	}
	var claims map[string]any
	if json.Unmarshal(body, &claims) != nil {
		return time.Time{}, errors.New("Personal JWT claims invalid")
	}
	authClaims, _ := claims["https://api.openai.com/auth"].(map[string]any)
	mfaClaims, _ := claims["https://api.openai.com/mfa"].(map[string]any)
	lookup := func(key string) string {
		for _, candidate := range []any{claims[key], authClaims[key], claims["https://api.openai.com/auth."+key]} {
			if value, ok := candidate.(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
		return ""
	}
	if lookup("client_id") != personalWebClientID || lookup("chatgpt_account_id") == "" || lookup("chatgpt_plan_type") == "k12" || lookup("chatgpt_plan_type") == "workspace" {
		return time.Time{}, errors.New("Personal token scope invalid")
	}
	scopeSet := map[string]bool{}
	for _, candidate := range []any{claims["scp"], authClaims["scp"], claims["scope"], authClaims["scope"], claims["https://api.openai.com/auth.scp"], claims["https://api.openai.com/auth.scope"]} {
		for _, scope := range personalClaimStrings(candidate) {
			scopeSet[scope] = true
		}
	}
	for _, required := range strings.Fields(personalWebScopes) {
		if !scopeSet[required] {
			return time.Time{}, errors.New("Personal token scopes incomplete")
		}
	}
	foundTOTP := false
	for _, candidate := range []any{claims["amr"], authClaims["amr"], claims["https://api.openai.com/auth.amr"]} {
		for _, value := range personalClaimStrings(candidate) {
			if strings.EqualFold(value, "urn:openai:amr:otp_totp") {
				foundTOTP = true
			}
		}
	}
	mfaRequired := false
	for _, candidate := range []any{mfaClaims["required"], claims["required"], claims["https://api.openai.com/mfa.required"]} {
		if candidate == true {
			mfaRequired = true
		}
		if value, ok := candidate.(string); ok && strings.EqualFold(strings.TrimSpace(value), "yes") {
			mfaRequired = true
		}
	}
	if !foundTOTP || !mfaRequired {
		return time.Time{}, errors.New("post-TOTP proof missing")
	}
	exp, ok := claims["exp"].(float64)
	if !ok || exp <= 0 {
		return time.Time{}, errors.New("Personal token expiration missing")
	}
	expires := time.Unix(int64(exp), 0).UTC()
	if !expires.After(now.Add(time.Minute)) || expires.After(now.Add(24*time.Hour)) {
		return time.Time{}, errors.New("Personal token expiration invalid")
	}
	return expires, nil
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
