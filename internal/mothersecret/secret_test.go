package mothersecret

import (
	"bytes"
	"testing"

	"github.com/google/uuid"
)

type testKeyRing struct{ key [32]byte }

func (r testKeyRing) Current() (uint16, [32]byte)      { return 3, r.key }
func (r testKeyRing) Lookup(v uint16) ([32]byte, bool) { return r.key, v == 3 }

func TestMotherSourceEnvelopeBindsFieldMotherRevisionAndKey(t *testing.T) {
	id := uuid.New()
	ring := testKeyRing{key: [32]byte{3}}
	sealed, err := Seal(ring, id, 2, Password, []byte("source-password"))
	if err != nil || !IsSealed(sealed) || bytes.Contains(sealed, []byte("source-password")) {
		t.Fatalf("seal: %v", err)
	}
	plain, err := Open(ring, id, 2, Password, sealed)
	if err != nil || string(plain) != "source-password" {
		t.Fatalf("open failed: %v", err)
	}
	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 1
	for _, input := range []struct {
		name     string
		ring     testKeyRing
		id       uuid.UUID
		revision int64
		field    Field
		data     []byte
	}{
		{"mother", ring, uuid.New(), 2, Password, sealed},
		{"revision", ring, id, 3, Password, sealed},
		{"field", ring, id, 2, TOTP, sealed},
		{"key", testKeyRing{key: [32]byte{8}}, id, 2, Password, sealed},
		{"plaintext", ring, id, 2, Password, []byte("source-password")},
		{"tamper", ring, id, 2, Password, tampered},
	} {
		t.Run(input.name, func(t *testing.T) {
			if _, err := Open(input.ring, input.id, input.revision, input.field, input.data); err == nil {
				t.Fatal("unauthenticated mother material accepted")
			}
		})
	}
	if _, err := Open(nil, id, 2, Password, sealed); err == nil {
		t.Fatal("missing key accepted")
	}
}
