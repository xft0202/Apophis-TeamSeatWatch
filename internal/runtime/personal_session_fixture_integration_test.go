//go:build integration

package runtime

import (
	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/accountsession"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

func sealSessionFor(kind string, ring auth.KeyRing, id uuid.UUID, revision int64, session platform.PersonalSession) (uint16, []byte, []byte, error) {
	return accountsession.Seal(kind, ring, id, revision, session)
}
