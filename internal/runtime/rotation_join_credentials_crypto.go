package runtime

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
)

type joinCredentialBinding struct {
	target, workspace, slot, attempt, generation uuid.UUID
	revision                                     int64
	platformWorkspace, kind                      string
}

func (b joinCredentialBinding) aad() []byte {
	// JSON preserves unambiguous field boundaries even for platform identifiers.
	raw, _ := json.Marshal([]any{"teamseatwatch:rotation-child-credential:v1", b.target, b.workspace, b.platformWorkspace, b.slot, b.attempt, b.generation, b.revision, b.kind})
	return raw
}
func joinCredentialCipher(ring auth.KeyRing, version uint16) (cipher.AEAD, error) {
	if ring == nil {
		return nil, fmt.Errorf("child credential key missing")
	}
	key, ok := ring.Lookup(version)
	if !ok {
		return nil, fmt.Errorf("child credential key unavailable")
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func sealJoinCredential(ring auth.KeyRing, b joinCredentialBinding, value any) (uint16, []byte, []byte, error) {
	if ring == nil {
		return 0, nil, nil, fmt.Errorf("child credential key missing")
	}
	version, _ := ring.Current()
	gcm, err := joinCredentialCipher(ring, version)
	if err != nil {
		return 0, nil, nil, err
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return 0, nil, nil, err
	}
	defer clear(payload)
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return 0, nil, nil, err
	}
	return version, nonce, gcm.Seal(nil, nonce, payload, b.aad()), nil
}
func openJoinCredential(ring auth.KeyRing, b joinCredentialBinding, version uint16, nonce, sealed []byte, out any) error {
	gcm, err := joinCredentialCipher(ring, version)
	if err != nil {
		return err
	}
	if len(nonce) != gcm.NonceSize() {
		return fmt.Errorf("child credential nonce invalid")
	}
	payload, err := gcm.Open(nil, nonce, sealed, b.aad())
	if err != nil {
		return fmt.Errorf("child credential seal invalid")
	}
	defer clear(payload)
	return json.Unmarshal(payload, out)
}
