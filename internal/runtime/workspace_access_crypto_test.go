package runtime

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

func TestSealedWorkspaceBearerBoundToMotherSpaceAndSourceGeneration(t *testing.T) {
	binding := workspaceAccessBinding{motherID: uuid.New(), workspaceID: uuid.New(), run: uuid.New(), generation: uuid.New(), revision: 3, attempt: 5, exchangeID: uuid.New()}
	access := platform.WorkspaceAccess{AccessToken: "secret-workspace-token", WorkspaceID: "canonical-space", SessionID: uuid.NewString(), DeviceID: "device", Cookies: []platform.SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "secret-cookie"}}, ExpiresAt: time.Now().Add(time.Hour)}
	version, nonce, sealed, err := sealWorkspaceAccess(personalTestKeyRing{}, binding, access)
	if err != nil {
		t.Fatal(err)
	}
	got, err := openWorkspaceAccess(personalTestKeyRing{}, binding, version, nonce, sealed)
	if err != nil || got.AccessToken != access.AccessToken || got.Cookies[0].Value != access.Cookies[0].Value {
		t.Fatalf("seal roundtrip failed: %+v %v", got, err)
	}
	for _, mutate := range []func(*workspaceAccessBinding){
		func(b *workspaceAccessBinding) { b.motherID = uuid.New() },
		func(b *workspaceAccessBinding) { b.workspaceID = uuid.New() },
		func(b *workspaceAccessBinding) { b.run = uuid.New() },
		func(b *workspaceAccessBinding) { b.generation = uuid.New() },
		func(b *workspaceAccessBinding) { b.revision++ },
		func(b *workspaceAccessBinding) { b.attempt++ },
		func(b *workspaceAccessBinding) { b.exchangeID = uuid.New() },
	} {
		changed := binding
		mutate(&changed)
		if _, err := openWorkspaceAccess(personalTestKeyRing{}, changed, version, nonce, sealed); err == nil {
			t.Fatal("bearer crossed AAD source boundary")
		}
	}
}
