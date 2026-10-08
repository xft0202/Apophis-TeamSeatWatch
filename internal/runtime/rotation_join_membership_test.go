package runtime

import (
	"fmt"
	"testing"

	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

func TestClassifyRotationMembershipEvidence(t *testing.T) {
	base := platform.Result{Outcome: platform.OutcomeOperational, Completeness: platform.Complete, Members: []platform.Member{{Kind: "member", PlatformMemberID: "member-1", Identifier: "Candidate@Example.test", Status: "active", SeatType: "prolite"}}}
	for _, tc := range []struct {
		name             string
		result           platform.Result
		err              error
		want, diagnostic string
	}{
		{name: "confirmed", result: base, want: "confirmed", diagnostic: "member_confirmed"},
		{name: "absent", result: platform.Result{Outcome: platform.OutcomeOperational, Completeness: platform.Complete}, want: "absent", diagnostic: "reconcile_absent"},
		{name: "incomplete", result: platform.Result{Outcome: platform.OutcomeIncomplete, Completeness: platform.Partial}, want: "unknown", diagnostic: "membership_incomplete"},
		{name: "unauthorized", result: platform.Result{Outcome: platform.OutcomeUnauthorized, Completeness: platform.Unknown}, want: "unknown", diagnostic: "membership_unauthorized"},
		{name: "pending", result: platform.Result{Outcome: platform.OutcomeOperational, Completeness: platform.Complete, Members: []platform.Member{{Kind: "pending_invite", Identifier: "candidate@example.test", Status: "pending", SeatType: "prolite"}}}, want: "invalid", diagnostic: "member_not_confirmed"},
		{name: "duplicate", result: platform.Result{Outcome: platform.OutcomeOperational, Completeness: platform.Complete, Members: []platform.Member{{Kind: "member", PlatformMemberID: "one", Identifier: "candidate@example.test", Status: "active", SeatType: "prolite"}, {Kind: "member", PlatformMemberID: "two", Identifier: "CANDIDATE@example.test", Status: "active", SeatType: "prolite"}}}, want: "unknown", diagnostic: "membership_duplicate_identifier"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyRotationMembership(tc.result, tc.err, "candidate@example.test", "prolite")
			if got.result != tc.want || got.diagnostic != tc.diagnostic || (tc.want == "confirmed" && got.memberID != "member-1") {
				t.Fatalf("observation=%+v", got)
			}
		})
	}
	if got := classifyRotationMembership(base, nil, "candidate@example.test", "prolite"); got.result != "confirmed" {
		t.Fatal(got)
	}
}

func TestClassifyRotationMembershipIdentityCollisions(t *testing.T) {
	for _, field := range []string{"member_id", "account_user_id"} {
		for _, present := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/candidate_present=%v", field, present), func(t *testing.T) {
				identifier := "candidate@example.test"
				if !present {
					identifier = "other@example.test"
				}
				members := []platform.Member{{Kind: "member", PlatformMemberID: "one", PlatformAccountUserID: "user-one", Identifier: identifier, Status: "active", SeatType: "prolite"}, {Kind: "member", PlatformMemberID: "two", PlatformAccountUserID: "user-two", Identifier: "second@example.test", Status: "active", SeatType: "prolite"}}
				if field == "member_id" {
					members[1].PlatformMemberID = members[0].PlatformMemberID
				} else {
					members[1].PlatformAccountUserID = members[0].PlatformAccountUserID
				}
				got := classifyRotationMembership(platform.Result{Outcome: platform.OutcomeOperational, Completeness: platform.Complete, Members: members}, nil, "candidate@example.test", "prolite")
				if got.result != "unknown" {
					t.Fatalf("ambiguous roster accepted: %+v", got)
				}
			})
		}
	}
	members := []platform.Member{{Kind: "member", PlatformMemberID: "one", Identifier: "candidate@example.test", Status: "active", SeatType: "prolite"}, {Kind: "member", PlatformMemberID: "two", Identifier: "other@example.test", Status: "active", SeatType: "prolite"}}
	if got := classifyRotationMembership(platform.Result{Outcome: platform.OutcomeOperational, Completeness: platform.Complete, Members: members}, nil, "candidate@example.test", "prolite"); got.result != "confirmed" {
		t.Fatalf("empty optional user IDs collided: %+v", got)
	}
}
