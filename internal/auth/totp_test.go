package auth

import (
	"testing"
	"time"
)

func TestTOTPCodeMatchesRFC6238SHA1Vector(t *testing.T) {
	code, err := TOTPCode("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", time.Unix(59, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if code != "287082" {
		t.Fatalf("code=%q want 287082", code)
	}
}

func TestTOTPCodeRejectsInvalidSecret(t *testing.T) {
	if _, err := TOTPCode("not-base32", time.Unix(59, 0).UTC()); err == nil {
		t.Fatal("invalid TOTP secret must be rejected")
	}
}

func TestCompleteMaterialAcceptsBase32OrExplicitSessionMarkers(t *testing.T) {
	now := time.Unix(59, 0).UTC()
	for _, valid := range []string{"GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", "cookie", "GOOGLE", " Session ", "none"} {
		if !CompleteMaterial(valid, now) {
			t.Fatalf("expected CompleteMaterial(%q) to be true", valid)
		}
	}
	for _, invalid := range []string{"", "not-base32", "bad!secret"} {
		if CompleteMaterial(invalid, now) {
			t.Fatalf("expected CompleteMaterial(%q) to be false", invalid)
		}
	}
}
