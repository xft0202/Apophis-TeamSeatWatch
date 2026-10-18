package platform

import (
	"context"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/browserenv"
	"time"
)

// PersonalSessionRenewer checks saved browser cookies without submitting login materials.
// invalid_login is returned only when the platform confirms that session is gone.
type PersonalSessionRenewer interface {
	RenewPersonal(context.Context, PersonalSession, browserenv.Environment) (PersonalRefreshResult, error)
}

// PersonalSessionPolicyRenewer extends PersonalSessionRenewer with explicit
// mother vs target/child scope policy so both value and pointer adapters share
// the same renewal rules.
type PersonalSessionPolicyRenewer interface {
	RenewPersonalWithPolicy(ctx context.Context, saved PersonalSession, environment browserenv.Environment, allowFederatedWorkspace bool) (PersonalRefreshResult, error)
}

// AcquirePersonal is the common login policy for explicit account actions.
// Transport failures never cause another password/MFA attempt.
func AcquirePersonal(ctx context.Context, adapter PersonalSessionRefresher, material MotherMaterial, saved PersonalSession) (PersonalRefreshResult, error) {
	now := time.Now()
	if ValidateSavedPersonalPolicy(saved, now, material.AllowFederatedWorkspace) {
		return PersonalRefreshResult{Status: "ready", Session: saved}, nil
	}
	if validPersonalCookies(saved) && (saved.SessionExpiresAt.IsZero() || saved.SessionExpiresAt.After(now)) {
		var (
			result PersonalRefreshResult
			err    error
		)
		allowFederated := material.AllowFederatedWorkspace && (isSavedFederatedSession(saved) || saved.AccessToken == "")
		if policyRenewer, ok := adapter.(PersonalSessionPolicyRenewer); ok {
			result, err = policyRenewer.RenewPersonalWithPolicy(ctx, saved, material.Environment, allowFederated)
		} else if renewer, ok := adapter.(PersonalSessionRenewer); ok {
			result, err = renewer.RenewPersonal(ctx, saved, material.Environment)
		} else {
			return PersonalRefreshResult{Status: "refresh_failed"}, ErrPersonalRefreshUnavailable
		}
		if err != nil || result.Status != "invalid_login" {
			return result, err
		}
		if !ValidatePersonalRefresh(result, time.Now()) {
			return PersonalRefreshResult{Status: "refresh_failed"}, nil
		}
	}
	return adapter.RefreshPersonal(ctx, material)
}
