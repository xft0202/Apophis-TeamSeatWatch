package runtime

import (
	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/accountsession"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

func sealPersonalSession(ring auth.KeyRing, id uuid.UUID, revision int64, session platform.PersonalSession) (uint16, []byte, []byte, error) {
	return accountsession.Seal("mother", ring, id, revision, session)
}
func openPersonalSession(ring auth.KeyRing, id uuid.UUID, revision int64, version uint16, nonce, ciphertext []byte) (platform.PersonalSession, error) {
	return accountsession.Open("mother", ring, id, revision, version, nonce, ciphertext)
}
func openSessionFor(kind string, ring auth.KeyRing, id uuid.UUID, revision int64, version uint16, nonce, ciphertext []byte) (platform.PersonalSession, error) {
	return accountsession.Open(kind, ring, id, revision, version, nonce, ciphertext)
}
