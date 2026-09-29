package platform

import (
	"context"
	"errors"
	"net/http"
)

// PersonalProbeEvidence must originate from an authenticated Personal endpoint.
// VerifiedDeactivation means the adapter checked an explicit structured upstream
// account-deactivation fact; a raw 403 body or arbitrary error string is not proof.
// This classifier is independent of the legacy password-backed target probe.
type PersonalProbeEvidence struct {
	HTTPStatus           int
	ErrorCode            string
	VerifiedDeactivation bool
	VerifiedUsage        bool
	TransportError       error
	Malformed            bool
}

type PersonalProbeOutcome string

const (
	PersonalAvailable         PersonalProbeOutcome = "available"
	PersonalMissingCredential PersonalProbeOutcome = "missing_personal_credential"
	PersonalCredentialInvalid PersonalProbeOutcome = "credential_invalid"
	PersonalForbidden         PersonalProbeOutcome = "forbidden"
	PersonalBanned            PersonalProbeOutcome = "banned"
	PersonalNetworkError      PersonalProbeOutcome = "network_error"
	PersonalUnknown           PersonalProbeOutcome = "unknown"
)

func ClassifyPersonalProbe(e PersonalProbeEvidence) PersonalProbeOutcome {
	if e.TransportError != nil {
		if errors.Is(e.TransportError, context.Canceled) {
			return PersonalUnknown
		}
		return PersonalNetworkError
	}
	if e.HTTPStatus == http.StatusUnauthorized {
		return PersonalCredentialInvalid
	}
	if e.HTTPStatus == http.StatusForbidden {
		if !e.Malformed && e.VerifiedDeactivation && e.ErrorCode == "account_deactivated" {
			return PersonalBanned
		}
		return PersonalForbidden
	}
	if e.HTTPStatus >= 200 && e.HTTPStatus < 300 && !e.Malformed && e.VerifiedUsage {
		return PersonalAvailable
	}
	return PersonalUnknown
}
