package mothersecret

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
)

// Only the exclusive-lock migration accepts legacy plaintext. All runtime
// readers reject material without this versioned envelope.
var prefix = []byte("TSWM1\x00")

type Field string

const (
	Password Field = "password"
	TOTP     Field = "totp"
)

func validInput(ring auth.KeyRing, id uuid.UUID, revision int64, field Field) bool {
	return ring != nil && id != uuid.Nil && revision > 0 && (field == Password || field == TOTP)
}

func associatedData(id uuid.UUID, revision int64, field Field) []byte {
	return []byte(fmt.Sprintf("teamseatwatch:mother-source:v1:%s:%d:%s", id, revision, field))
}

func IsSealed(material []byte) bool { return bytes.HasPrefix(material, prefix) }

func Seal(ring auth.KeyRing, id uuid.UUID, revision int64, field Field, plaintext []byte) ([]byte, error) {
	if !validInput(ring, id, revision, field) || len(plaintext) == 0 {
		return nil, errors.New("mother material seal input invalid")
	}
	version, key := ring.Current()
	if version == 0 {
		return nil, errors.New("mother material key version invalid")
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, errors.New("mother material cipher unavailable")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("mother material cipher unavailable")
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, errors.New("mother material nonce unavailable")
	}
	sealed := make([]byte, 0, len(prefix)+2+len(nonce)+len(plaintext)+gcm.Overhead())
	sealed = append(sealed, prefix...)
	sealed = binary.BigEndian.AppendUint16(sealed, version)
	sealed = append(sealed, nonce...)
	sealed = gcm.Seal(sealed, nonce, plaintext, associatedData(id, revision, field))
	return sealed, nil
}

func Open(ring auth.KeyRing, id uuid.UUID, revision int64, field Field, sealed []byte) ([]byte, error) {
	if !validInput(ring, id, revision, field) || !IsSealed(sealed) || len(sealed) < len(prefix)+2+12+16+1 {
		return nil, errors.New("mother material envelope invalid")
	}
	version := binary.BigEndian.Uint16(sealed[len(prefix):])
	key, ok := ring.Lookup(version)
	if !ok || version == 0 {
		return nil, errors.New("mother material key unavailable")
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, errors.New("mother material cipher unavailable")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("mother material cipher unavailable")
	}
	nonceStart := len(prefix) + 2
	if len(sealed) < nonceStart+gcm.NonceSize()+gcm.Overhead()+1 {
		return nil, errors.New("mother material envelope invalid")
	}
	plaintext, err := gcm.Open(nil, sealed[nonceStart:nonceStart+gcm.NonceSize()], sealed[nonceStart+gcm.NonceSize():], associatedData(id, revision, field))
	if err != nil || len(plaintext) == 0 {
		return nil, errors.New("mother material authentication failed")
	}
	return plaintext, nil
}
