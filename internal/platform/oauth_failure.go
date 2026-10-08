package platform

import (
	"errors"
	"fmt"
	"regexp"
)

// OAuthFailure exposes a bounded diagnostic, never upstream bodies, URLs,
// authorization codes or cookie values.
type OAuthFailure struct {
	Code       string
	HTTPStatus int
	cause      error
}

var oauthDiagnosticPattern = regexp.MustCompile(`^oauth_(browser|workspace_select|authorize|token_exchange|identity)_(failed|browser_challenge|http_[1-5][0-9]{2})$`)

func normalizedOAuthDiagnostic(code string) string {
	switch code {
	case "oauth_session_expired", "oauth_workspace_unavailable", "oauth_identity_failed":
		return code
	}
	if oauthDiagnosticPattern.MatchString(code) {
		return code
	}
	return ""
}

func (e *OAuthFailure) Error() string { return e.Code }
func (e *OAuthFailure) Unwrap() error { return e.cause }
func oauthFailure(stage string, err error) *OAuthFailure {
	code := "oauth_" + stage + "_failed"
	status := 0
	var rejected *authHTTPError
	if errors.As(err, &rejected) {
		status = rejected.status
		if rejected.browserChallenge {
			code = "oauth_" + stage + "_browser_challenge"
		} else {
			code = fmt.Sprintf("oauth_%s_http_%d", stage, status)
		}
	}
	if errors.Is(err, ErrBrowserSessionExpired) {
		code = "oauth_session_expired"
	}
	if errors.Is(err, ErrOAuthWorkspaceUnavailable) {
		code = "oauth_workspace_unavailable"
	}
	return &OAuthFailure{Code: code, HTTPStatus: status, cause: err}
}
func OAuthFailureCode(err error) string {
	var failure *OAuthFailure
	if errors.As(err, &failure) {
		return failure.Code
	}
	if errors.Is(err, ErrBrowserSessionExpired) {
		return "oauth_session_expired"
	}
	if errors.Is(err, ErrPersonalIdentityUnavailable) {
		return "oauth_identity_failed"
	}
	return "oauth_generation_failed"
}
