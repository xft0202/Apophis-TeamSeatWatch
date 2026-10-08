package platform

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestClassifyPersonalProbeExplicitEvidenceOnly(t *testing.T) {
	cases := []struct {
		name     string
		evidence PersonalProbeEvidence
		want     PersonalProbeOutcome
	}{
		{"401", PersonalProbeEvidence{HTTPStatus: 401}, PersonalCredentialInvalid},
		{"ordinary 403", PersonalProbeEvidence{HTTPStatus: 403}, PersonalForbidden},
		{"unverified code", PersonalProbeEvidence{HTTPStatus: 403, ErrorCode: "account_deactivated"}, PersonalForbidden},
		{"verified wrong code", PersonalProbeEvidence{HTTPStatus: 403, ErrorCode: "generic_forbidden", VerifiedDeactivation: true}, PersonalForbidden},
		{"malformed", PersonalProbeEvidence{HTTPStatus: 403, ErrorCode: "account_deactivated", VerifiedDeactivation: true, Malformed: true}, PersonalForbidden},
		{"verified explicit", PersonalProbeEvidence{HTTPStatus: 403, ErrorCode: "account_deactivated", VerifiedDeactivation: true}, PersonalBanned},
		{"network", PersonalProbeEvidence{TransportError: errors.New("dial failed")}, PersonalNetworkError},
		{"canceled", PersonalProbeEvidence{TransportError: context.Canceled}, PersonalUnknown},
		{"unknown", PersonalProbeEvidence{HTTPStatus: http.StatusOK, Malformed: true}, PersonalUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyPersonalProbe(tc.evidence); got != tc.want {
				t.Fatalf("%s != %s", got, tc.want)
			}
		})
	}
}
