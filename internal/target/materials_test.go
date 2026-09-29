package target

import (
	"bytes"
	"testing"

	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
)

type materialsTestRing struct{ key [32]byte }

func (r materialsTestRing) Current() (uint16, [32]byte)            { return 9, r.key }
func (r materialsTestRing) Lookup(version uint16) ([32]byte, bool) { return r.key, version == 9 }

func TestSealMaterialDoesNotPersistPlaintextAndRoundTrips(t *testing.T) {
	ring := materialsTestRing{key: [32]byte{7}}
	secret := "child-password-123"
	sealed, err := SealMaterial(secret, ring)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte(secret)) {
		t.Fatal("sealed material contains plaintext")
	}
	got, err := OpenMaterial(sealed, ring)
	if err != nil {
		t.Fatal(err)
	}
	if got != secret {
		t.Fatalf("round trip = %q", got)
	}
}

func TestOpenMaterialRejectsUnsealedBytes(t *testing.T) {
	if _, err := OpenMaterial([]byte("legacy-password"), materialsTestRing{}); err == nil {
		t.Fatal("plaintext material was accepted")
	}
}

func TestCompleteTOTPSeparatesRepairableMaterial(t *testing.T) {
	if CompleteTOTP("") || CompleteTOTP("not-base32") {
		t.Fatal("invalid TOTP was marked complete")
	}
	if !CompleteTOTP("JBSWY3DPEHPK3PXP") {
		t.Fatal("valid TOTP was not marked complete")
	}
	var _ auth.KeyRing = materialsTestRing{}
}
