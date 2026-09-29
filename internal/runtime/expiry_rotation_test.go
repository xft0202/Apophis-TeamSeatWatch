package runtime

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

func TestExpiryRotationDeadlineBoundary(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if rotationExpired(now.Add(time.Nanosecond), now) || !rotationExpired(now, now) || !rotationExpired(now.Add(-time.Nanosecond), now) {
		t.Fatal("subscription deadline is inclusive at the instant of expiry")
	}
}

func TestExpiryRotationProofBoundaries(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	proof := rotationProof{Source: "mock_write_permission", EvidenceID: "permission-1", ObservedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute)}
	if !validRotationProof(proof, "mock_write_permission", now) {
		t.Fatal("complete independent proof rejected")
	}
	for _, change := range []func(*rotationProof){
		func(p *rotationProof) { p.Source = "official_readonly" },
		func(p *rotationProof) { p.EvidenceID = "" },
		func(p *rotationProof) { p.ObservedAt = now.Add(-5*time.Minute - time.Nanosecond) },
		func(p *rotationProof) { p.ObservedAt = now.Add(time.Nanosecond) },
		func(p *rotationProof) { p.ExpiresAt = now },
		func(p *rotationProof) { p.ExpiresAt = now.Add(6 * time.Minute) },
	} {
		altered := proof
		change(&altered)
		if validRotationProof(altered, "mock_write_permission", now) {
			t.Fatalf("accepted invalid proof %+v", altered)
		}
	}
}
func TestExpiryRotationRequiresExplicitCompatibleAssignments(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	p := ownerapi.ExpiryRotationPreview{Slots: []ownerapi.ExpiryRotationSlot{{PlatformMemberId: "slot-a", SeatType: "default", Decision: "replaceable"}, {PlatformMemberId: "slot-b", SeatType: "default", Decision: "replaceable"}}, Candidates: []ownerapi.ExpiryRotationCandidate{{AccountId: a, SeatType: "default", Decision: "eligible"}, {AccountId: b, SeatType: "default", Decision: "eligible"}}}
	choices := []ownerapi.ExpiryRotationAssignment{{PlatformMemberId: "slot-b", AccountId: b}, {PlatformMemberId: "slot-a", AccountId: a}}
	mapped, valid := rotationAssignments(p, choices)
	if !valid || mapped[0].PlatformMemberId != "slot-a" {
		t.Fatal("explicit matching choices rejected")
	}
	for _, bad := range [][]ownerapi.ExpiryRotationAssignment{nil, choices[:1], {{PlatformMemberId: "slot-a", AccountId: a}, {PlatformMemberId: "slot-b", AccountId: a}}, {{PlatformMemberId: "slot-a", AccountId: a}, {PlatformMemberId: "slot-a", AccountId: b}}, {{PlatformMemberId: "other", AccountId: a}, {PlatformMemberId: "slot-b", AccountId: b}}} {
		if _, ok := rotationAssignments(p, bad); ok {
			t.Fatalf("accepted incompatible assignment %+v", bad)
		}
	}
	p.Candidates[1].SeatType = "usage_based"
	if _, ok := rotationAssignments(p, choices); ok {
		t.Fatal("mismatched seat type accepted")
	}
	p.Candidates[1].SeatType = "default"
	p.Candidates = append(p.Candidates, ownerapi.ExpiryRotationCandidate{AccountId: uuid.New(), SeatType: "default", Decision: "eligible"})
	if _, ok := rotationAssignments(p, choices); ok {
		t.Fatal("excess eligible candidates silently narrowed")
	}
}

func TestExpiryRotationDigestBindsExactScope(t *testing.T) {
	p := ownerapi.ExpiryRotationPreview{DraftId: uuid.New(), DraftVersion: 1, WorkspaceId: uuid.New(), SourceRevisions: map[string]int64{"account": 1}, Members: []string{"old@example.test · id-1"}, Invitations: []string{"new@example.test · pending"}, Slots: []ownerapi.ExpiryRotationSlot{{Identifier: "old@example.test", PlatformMemberId: "id-1", SeatType: "default", Decision: "replaceable", Reason: "mock-unprotected"}}, Candidates: []ownerapi.ExpiryRotationCandidate{{AccountId: uuid.New(), Decision: "eligible", Reason: "invited", SeatType: "default"}}}
	digest := rotationDigest(p)
	p.Id = uuid.New()
	p.Authorized = true
	now := time.Now()
	p.AuthorizedAt = &now
	if rotationDigest(p) != digest {
		t.Fatal("record metadata changed frozen facts")
	}
	p.SourceRevisions["account"] = 2
	if rotationDigest(p) == digest {
		t.Fatal("changed source revision reuses preview")
	}
}
