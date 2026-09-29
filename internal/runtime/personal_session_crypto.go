package runtime

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

func personalSessionAAD(kind string, id uuid.UUID, revision int64) []byte {
	return []byte(fmt.Sprintf("teamseatwatch:%s-personal-session:v1:%s:%d", kind, id, revision))
}

func sealPersonalSession(ring auth.KeyRing, id uuid.UUID, revision int64, session platform.PersonalSession) (uint16, []byte, []byte, error) {
	return sealSessionFor("mother", ring, id, revision, session)
}

func sealSessionFor(kind string, ring auth.KeyRing, id uuid.UUID, revision int64, session platform.PersonalSession) (uint16, []byte, []byte, error) {
	if ring == nil {
		return 0, nil, nil, errors.New("personal session key ring missing")
	}
	payload, err := json.Marshal(session)
	if err != nil {
		return 0, nil, nil, err
	}
	version, key := ring.Current()
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return 0, nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return 0, nil, nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return 0, nil, nil, err
	}
	return version, nonce, gcm.Seal(nil, nonce, payload, personalSessionAAD(kind, id, revision)), nil
}

func openPersonalSession(ring auth.KeyRing, id uuid.UUID, revision int64, version uint16, nonce, ciphertext []byte) (platform.PersonalSession, error) {
	return openSessionFor("mother", ring, id, revision, version, nonce, ciphertext)
}

func openSessionFor(kind string, ring auth.KeyRing, id uuid.UUID, revision int64, version uint16, nonce, ciphertext []byte) (platform.PersonalSession, error) {
	if ring == nil {
		return platform.PersonalSession{}, errors.New("personal session key ring missing")
	}
	key, ok := ring.Lookup(version)
	if !ok {
		return platform.PersonalSession{}, errors.New("personal session key not found")
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return platform.PersonalSession{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return platform.PersonalSession{}, err
	}
	if len(nonce) != gcm.NonceSize() {
		return platform.PersonalSession{}, errors.New("personal session nonce invalid")
	}
	payload, err := gcm.Open(nil, nonce, ciphertext, personalSessionAAD(kind, id, revision))
	if err != nil {
		return platform.PersonalSession{}, err
	}
	var session platform.PersonalSession
	if err := json.Unmarshal(payload, &session); err != nil {
		return platform.PersonalSession{}, err
	}
	return session, nil
}
