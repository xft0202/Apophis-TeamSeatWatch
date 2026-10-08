package runtime

import (
	"testing"

	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

func TestValidSelectedEntriesRequireCompleteNonduplicatedRoster(t *testing.T) {
	members := []platform.Member{
		{Kind: "member", PlatformMemberID: "platform-child", Identifier: "child@example.test", Status: "active"},
		{Kind: "pending_invite", Identifier: "invite@example.test", Status: "pending"},
	}
	if !validSelectedEntries(members, 1, 1) {
		t.Fatal("complete roster rejected")
	}
	if validSelectedEntries(members, 2, 1) {
		t.Fatal("short roster accepted")
	}
	if validSelectedEntries(members, 1, 0) {
		t.Fatal("invitation count ignored")
	}
	if validSelectedEntries(append(members, members[0]), 2, 1) {
		t.Fatal("duplicate roster accepted")
	}
	if validSelectedEntries([]platform.Member{{Kind: "member", Identifier: "child@example.test", Status: "active"}}, 1, 0) {
		t.Fatal("member without platform ID accepted")
	}
}
