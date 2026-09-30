package runtime

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

func TestExpiryRotationUsageAndGlobalProtectionAreScopedAndSticky(t *testing.T) {
	now := time.Now().UTC()
	workspace, account := uuid.New(), uuid.New()
	source := rotationProof{Source: "mock_workspace_usage", EvidenceID: "usage-1", ObservedAt: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute)}
	never := rotationUsageProof{rotationProof: source, WorkspaceID: workspace, AccountID: account, State: "never_used", EverUsed: false}
	if !validRotationUsage(never, workspace, account, now) {
		t.Fatal("fresh scoped never-used evidence rejected")
	}
	never.EverUsed = true
	if validRotationUsage(never, workspace, account, now) {
		t.Fatal("sticky ever-used downgraded")
	}
	never.EverUsed = false
	never.WorkspaceID = uuid.New()
	if validRotationUsage(never, workspace, account, now) {
		t.Fatal("other Workspace usage accepted")
	}
	never.WorkspaceID = workspace
	never.AccountID = uuid.New()
	if validRotationUsage(never, workspace, account, now) {
		t.Fatal("other account usage accepted")
	}
	protection := rotationProtectionProof{rotationProof: rotationProof{Source: "mock_global_protection", EvidenceID: "global-1", ObservedAt: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute)}, AccountID: account, Status: "none"}
	if !validRotationProtection(protection, account, now) {
		t.Fatal("fresh global protection source rejected")
	}
	for _, status := range []string{"sale_reserved", "delivery_pending", "suspected_sold", "unknown"} {
		if !blockingRotationProtection(status) {
			t.Fatalf("%s must block write", status)
		}
	}
	for _, status := range []string{"delivered", "canceled_retired"} {
		protection.Status = status
		if !validRotationProtection(protection, account, now) || blockingRotationProtection(status) {
			t.Fatalf("%s must retain rather than certify replaceability", status)
		}
	}
	protection.Status = "none"
	protection.AccountID = uuid.New()
	if validRotationProtection(protection, account, now) {
		t.Fatal("another account protection accepted")
	}
}

func TestExpiryRotationPrejoinAbsenceRequiresCompleteCurrentScopedLookup(t *testing.T) {
	now := time.Now().UTC()
	workspace, account, mother, generation := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	proof := rotationProof{Source: "mock_usage_ledger_lookup", EvidenceID: "negative-ledger-lookup", ObservedAt: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute)}
	absence := &rotationUsageAbsenceProof{rotationProof: proof, WorkspaceID: workspace, AccountID: account, MotherID: mother, SessionGeneration: generation, VerificationID: 42, Identifier: "child@rotate.test", AccountVersion: 3, CredentialVersion: 2, MembershipVersion: 1, LookupComplete: true, FirstUseRecordStatus: "absent"}
	usage := rotationUsageProof{rotationProof: rotationProof{Source: "mock_workspace_usage", EvidenceID: "usage-check", ObservedAt: proof.ObservedAt, ExpiresAt: proof.ExpiresAt}, WorkspaceID: workspace, AccountID: account, State: "unobserved_prejoin", Absence: absence}
	scope := rotationCandidateScope{WorkspaceID: workspace, AccountID: account, MotherID: mother, SessionGeneration: generation, VerificationID: 42, Identifier: "child@rotate.test", AccountVersion: 3, CredentialVersion: 2, MembershipVersion: 1}
	if !validRotationCandidateUsage(usage, scope, now) {
		t.Fatal("complete scoped absence rejected")
	}
	for name, change := range map[string]func(*rotationUsageProof){
		"missing":          func(u *rotationUsageProof) { u.Absence = nil },
		"incomplete":       func(u *rotationUsageProof) { u.Absence.LookupComplete = false },
		"record_exists":    func(u *rotationUsageProof) { u.Absence.FirstUseRecordStatus = "present" },
		"ever_used":        func(u *rotationUsageProof) { u.EverUsed = true },
		"unknown":          func(u *rotationUsageProof) { u.State = "unknown" },
		"wrong_workspace":  func(u *rotationUsageProof) { u.Absence.WorkspaceID = uuid.New() },
		"wrong_account":    func(u *rotationUsageProof) { u.Absence.AccountID = uuid.New() },
		"wrong_mother":     func(u *rotationUsageProof) { u.Absence.MotherID = uuid.New() },
		"wrong_generation": func(u *rotationUsageProof) { u.Absence.SessionGeneration = uuid.New() },
		"old_verification": func(u *rotationUsageProof) { u.Absence.VerificationID-- },
		"wrong_identifier": func(u *rotationUsageProof) { u.Absence.Identifier = "other@rotate.test" },
		"old_account":      func(u *rotationUsageProof) { u.Absence.AccountVersion-- },
		"old_credential":   func(u *rotationUsageProof) { u.Absence.CredentialVersion-- },
		"old_membership":   func(u *rotationUsageProof) { u.Absence.MembershipVersion-- },
		"wrong_source":     func(u *rotationUsageProof) { u.Absence.Source = "mock_workspace_usage" },
		"expired":          func(u *rotationUsageProof) { u.Absence.ExpiresAt = now },
	} {
		t.Run(name, func(t *testing.T) {
			bad := usage
			copyProof := *absence
			bad.Absence = &copyProof
			change(&bad)
			if validRotationCandidateUsage(bad, scope, now) {
				t.Fatal("unproved absence admitted")
			}
		})
	}
}

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
