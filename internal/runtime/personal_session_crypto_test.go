package runtime

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

type personalTestKeyRing struct{ key [32]byte }

func (r personalTestKeyRing) Current() (uint16, [32]byte)            { return 1, r.key }
func (r personalTestKeyRing) Lookup(version uint16) ([32]byte, bool) { return r.key, version == 1 }

func TestSealPersonalSessionBindsMotherAndRevision(t *testing.T) {
	id := uuid.New()
	ring := personalTestKeyRing{}
	session := platform.PersonalSession{AccessToken: "secret-token", DeviceID: "device", Cookies: []platform.SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "secret-cookie"}}, ExpiresAt: time.Now().Add(time.Hour)}
	version, nonce, sealed, err := sealPersonalSession(ring, id, 2, session)
	if err != nil {
		t.Fatal(err)
	}
	got, err := openPersonalSession(ring, id, 2, version, nonce, sealed)
	if err != nil || got.AccessToken != session.AccessToken || got.Cookies[0].Value != session.Cookies[0].Value {
		t.Fatalf("roundtrip: %+v %v", got, err)
	}
	for _, input := range []struct {
		id       uuid.UUID
		revision int64
	}{{uuid.New(), 2}, {id, 3}} {
		if _, err := openPersonalSession(ring, input.id, input.revision, version, nonce, sealed); err == nil {
			t.Fatal("cross-account or old-generation session accepted")
		}
	}
}
