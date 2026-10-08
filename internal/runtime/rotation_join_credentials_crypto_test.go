package runtime

import (
	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
	"testing"
)

func TestRotationJoinCredentialCryptoIsolation(t *testing.T) {
	b := joinCredentialBinding{target: uuid.New(), workspace: uuid.New(), slot: uuid.New(), attempt: uuid.New(), generation: uuid.New(), revision: 1, platformWorkspace: "original", kind: "oauth"}
	original := platform.DeliveryCredentialSet{RefreshToken: "secret-refresh"}
	v, n, c, err := sealJoinCredential(personalTestKeyRing{}, b, original)
	if err != nil {
		t.Fatal(err)
	}
	var got platform.DeliveryCredentialSet
	if err = openJoinCredential(personalTestKeyRing{}, b, v, n, c, &got); err != nil || got.RefreshToken != original.RefreshToken {
		t.Fatal(err)
	}
	for _, field := range []string{"target", "workspace", "slot", "attempt", "generation", "revision", "platform", "kind"} {
		changed := b
		switch field {
		case "target":
			changed.target = uuid.New()
		case "workspace":
			changed.workspace = uuid.New()
		case "slot":
			changed.slot = uuid.New()
		case "attempt":
			changed.attempt = uuid.New()
		case "generation":
			changed.generation = uuid.New()
		case "revision":
			changed.revision++
		case "platform":
			changed.platformWorkspace = "other"
		case "kind":
			changed.kind = "web"
		}
		if openJoinCredential(personalTestKeyRing{}, changed, v, n, c, &got) == nil {
			t.Fatalf("wrong %s AAD accepted", field)
		}
	}
}

func TestRotationJoinCredentialCryptoWrongKey(t *testing.T) {
	b := joinCredentialBinding{target: uuid.New(), workspace: uuid.New(), slot: uuid.New(), attempt: uuid.New(), generation: uuid.New(), revision: 1, platformWorkspace: "original", kind: "web"}
	ring := personalTestKeyRing{}
	v, n, c, err := sealJoinCredential(ring, b, platform.WorkspaceAccess{AccessToken: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	wrong := ring
	wrong.key[0] = 1
	for _, test := range []struct {
		ring          personalTestKeyRing
		version       uint16
		nonce, sealed []byte
	}{{wrong, v, n, c}, {ring, v + 1, n, c}, {ring, v, n[:2], c}, {ring, v, n, c[:2]}} {
		var got platform.WorkspaceAccess
		if err = openJoinCredential(test.ring, b, test.version, test.nonce, test.sealed, &got); err == nil {
			t.Fatal("wrong key/version/shape accepted")
		}
	}
}
