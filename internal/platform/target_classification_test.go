package platform

import (
	"net/http"
	"testing"
)

func TestTargetProbeKeepsForbiddenSeparateFromInvalidCredentials(t *testing.T) {
	if got := ClassifyTargetProbe(http.StatusUnauthorized, "", nil, false); got != TargetCredentialInvalid {
		t.Fatalf("401 = %q; want credential invalid", got)
	}
	for _, malformed := range []bool{false, true} {
		if got := ClassifyTargetProbe(http.StatusForbidden, "", nil, malformed); got != TargetAccountProblem {
			t.Fatalf("generic 403 (malformed=%v) = %q; want account problem", malformed, got)
		}
	}
	if got := ClassifyTargetProbe(http.StatusUnauthorized, "", nil, true); got != TargetCredentialInvalid {
		t.Fatalf("401 with unreadable body = %q; want credential invalid", got)
	}
	if got := ClassifyTargetProbe(http.StatusForbidden, "account_deactivated", nil, false); got != TargetDefinitelyUnavailable {
		t.Fatalf("structured deactivation = %q; want definite unavailability", got)
	}
}
