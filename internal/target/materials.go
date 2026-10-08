package target

import (
	"bytes"
	"encoding/binary"
	"errors"
	"time"

	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
)

var materialPrefix = []byte("TSW-SEALED-01\x00")

// SealMaterial stores versioned AES-GCM ciphertext, never the submitted secret.
func SealMaterial(value string, ring auth.KeyRing) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	version, nonce, ciphertext, err := auth.EncryptTOTP([]byte(value), ring)
	if err != nil {
		return nil, err
	}
	out := append([]byte{}, materialPrefix...)
	out = binary.BigEndian.AppendUint16(out, version)
	out = append(out, nonce...)
	return append(out, ciphertext...), nil
}

// OpenMaterial rejects unsealed data. Existing plaintext must be migrated before
// workers start; a missing key or tampered ciphertext must never reach a platform call.
func OpenMaterial(value []byte, ring auth.KeyRing) (string, error) {
	if len(value) == 0 {
		return "", nil
	}
	if ring == nil || !bytes.HasPrefix(value, materialPrefix) || len(value) < len(materialPrefix)+2+12+16 {
		return "", errors.New("unsealed or invalid account material")
	}
	offset := len(materialPrefix)
	plaintext, err := auth.DecryptTOTP(binary.BigEndian.Uint16(value[offset:offset+2]), value[offset+2:offset+14], value[offset+14:], ring)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func Sealed(value []byte) bool { return bytes.HasPrefix(value, materialPrefix) }

func CompleteTOTP(value string) bool {
	if value == "" {
		return false
	}
	_, err := auth.TOTPCode(value, time.Now())
	return err == nil
}
