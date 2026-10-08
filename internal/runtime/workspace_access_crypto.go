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

type workspaceAccessBinding struct {
	motherID, workspaceID       uuid.UUID
	run, generation, exchangeID uuid.UUID
	revision, attempt           int64
}

func (b workspaceAccessBinding) aad() []byte {
	return []byte(fmt.Sprintf("teamseatwatch:workspace-access:v1:%s:%s:%s:%s:%d:%s:%d",
		b.motherID, b.workspaceID, b.run, b.generation, b.revision, b.exchangeID, b.attempt))
}

func sealWorkspaceAccess(ring auth.KeyRing, binding workspaceAccessBinding, access platform.WorkspaceAccess) (uint16, []byte, []byte, error) {
	if ring == nil {
		return 0, nil, nil, errors.New("workspace access key missing")
	}
	payload, err := json.Marshal(access)
	if err != nil {
		return 0, nil, nil, err
	}
	defer clear(payload)
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
	return version, nonce, gcm.Seal(nil, nonce, payload, binding.aad()), nil
}

func openWorkspaceAccess(ring auth.KeyRing, binding workspaceAccessBinding, keyVersion uint16, nonce, ciphertext []byte) (platform.WorkspaceAccess, error) {
	if ring == nil {
		return platform.WorkspaceAccess{}, errors.New("workspace access key missing")
	}
	key, ok := ring.Lookup(keyVersion)
	if !ok {
		return platform.WorkspaceAccess{}, errors.New("workspace access key unavailable")
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return platform.WorkspaceAccess{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(nonce) != gcm.NonceSize() {
		return platform.WorkspaceAccess{}, errors.New("workspace access nonce invalid")
	}
	payload, err := gcm.Open(nil, nonce, ciphertext, binding.aad())
	if err != nil {
		return platform.WorkspaceAccess{}, err
	}
	defer clear(payload)
	var access platform.WorkspaceAccess
	if err = json.Unmarshal(payload, &access); err != nil {
		return platform.WorkspaceAccess{}, err
	}
	return access, nil
}
