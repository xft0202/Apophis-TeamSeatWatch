package runtime

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
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
	absence.EvidenceID = rotationAbsenceEvidenceID(*absence)
	usage := rotationUsageProof{rotationProof: rotationProof{Source: "mock_workspace_usage", EvidenceID: "usage-check", ObservedAt: proof.ObservedAt, ExpiresAt: proof.ExpiresAt}, WorkspaceID: workspace, AccountID: account, State: "unobserved_prejoin", Absence: absence}
	scope := rotationCandidateScope{WorkspaceID: workspace, AccountID: account, MotherID: mother, SessionGeneration: generation, VerificationID: 42, Identifier: "child@rotate.test", AccountVersion: 3, CredentialVersion: 2, MembershipVersion: 1}
	if !validRotationCandidateUsage(usage, scope, now) {
		t.Fatal("complete scoped absence rejected")
	}
	legacy := usage
	legacy.State = "never_used"
	legacy.Absence = nil
	if validRotationCandidateUsage(legacy, scope, now) {
		t.Fatal("legacy never_used without independent absence lookup admitted")
	}
	legacy.Absence = absence
	if !validRotationCandidateUsage(legacy, scope, now) {
		t.Fatal("never_used with complete independent absence lookup rejected")
	}
	changed := usage
	changedScope := scope
	changedScope.AccountID = uuid.New()
	changed.AccountID = changedScope.AccountID
	changedAbsence := *absence
	changedAbsence.AccountID = changedScope.AccountID
	changed.Absence = &changedAbsence
	if validRotationCandidateUsage(changed, changedScope, now) {
		t.Fatal("reused lookup ID on otherwise matching different account accepted")
	}
	changedAbsence.EvidenceID = rotationAbsenceEvidenceID(changedAbsence)
	if !validRotationCandidateUsage(changed, changedScope, now) {
		t.Fatal("new content-addressed lookup ID rejected for matching account")
	}
	for name, change := range map[string]func(*rotationUsageProof){
		"missing":              func(u *rotationUsageProof) { u.Absence = nil },
		"incomplete":           func(u *rotationUsageProof) { u.Absence.LookupComplete = false },
		"record_exists":        func(u *rotationUsageProof) { u.Absence.FirstUseRecordStatus = "present" },
		"ever_used":            func(u *rotationUsageProof) { u.EverUsed = true },
		"unknown":              func(u *rotationUsageProof) { u.State = "unknown" },
		"wrong_workspace":      func(u *rotationUsageProof) { u.Absence.WorkspaceID = uuid.New() },
		"wrong_account":        func(u *rotationUsageProof) { u.Absence.AccountID = uuid.New() },
		"wrong_mother":         func(u *rotationUsageProof) { u.Absence.MotherID = uuid.New() },
		"wrong_generation":     func(u *rotationUsageProof) { u.Absence.SessionGeneration = uuid.New() },
		"old_verification":     func(u *rotationUsageProof) { u.Absence.VerificationID-- },
		"wrong_identifier":     func(u *rotationUsageProof) { u.Absence.Identifier = "other@rotate.test" },
		"old_account":          func(u *rotationUsageProof) { u.Absence.AccountVersion-- },
		"old_credential":       func(u *rotationUsageProof) { u.Absence.CredentialVersion-- },
		"old_membership":       func(u *rotationUsageProof) { u.Absence.MembershipVersion-- },
		"wrong_source":         func(u *rotationUsageProof) { u.Absence.Source = "mock_workspace_usage" },
		"expired":              func(u *rotationUsageProof) { u.Absence.ExpiresAt = now },
		"reused_id_new_time":   func(u *rotationUsageProof) { u.Absence.ObservedAt = now.Add(-2 * time.Second) },
		"reused_id_new_result": func(u *rotationUsageProof) { u.Absence.FirstUseRecordStatus = "unknown" },
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

func TestExpiryRotationAbsenceIDBindsCompleteCanonicalPayload(t *testing.T) {
	now := time.Now().UTC()
	a := rotationUsageAbsenceProof{rotationProof: rotationProof{Source: "mock_usage_ledger_lookup", ObservedAt: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute)}, WorkspaceID: uuid.New(), AccountID: uuid.New(), MotherID: uuid.New(), SessionGeneration: uuid.New(), VerificationID: 42, Identifier: "child@rotate.test", AccountVersion: 3, CredentialVersion: 2, MembershipVersion: 1, LookupComplete: true, FirstUseRecordStatus: "absent"}
	originalID := rotationAbsenceEvidenceID(a)
	if len(originalID) != 64 {
		t.Fatalf("invalid digest length: %s", originalID)
	}
	for name, change := range map[string]func(*rotationUsageAbsenceProof){
		"source":              func(p *rotationUsageAbsenceProof) { p.Source = "other" },
		"completeness":        func(p *rotationUsageAbsenceProof) { p.LookupComplete = false },
		"result":              func(p *rotationUsageAbsenceProof) { p.FirstUseRecordStatus = "present" },
		"workspace":           func(p *rotationUsageAbsenceProof) { p.WorkspaceID = uuid.New() },
		"account":             func(p *rotationUsageAbsenceProof) { p.AccountID = uuid.New() },
		"mother":              func(p *rotationUsageAbsenceProof) { p.MotherID = uuid.New() },
		"generation":          func(p *rotationUsageAbsenceProof) { p.SessionGeneration = uuid.New() },
		"verification":        func(p *rotationUsageAbsenceProof) { p.VerificationID++ },
		"identifier":          func(p *rotationUsageAbsenceProof) { p.Identifier = "other@rotate.test" },
		"account_revision":    func(p *rotationUsageAbsenceProof) { p.AccountVersion++ },
		"credential_revision": func(p *rotationUsageAbsenceProof) { p.CredentialVersion++ },
		"membership_revision": func(p *rotationUsageAbsenceProof) { p.MembershipVersion++ },
		"observed_at":         func(p *rotationUsageAbsenceProof) { p.ObservedAt = p.ObservedAt.Add(time.Nanosecond) },
		"expires_at":          func(p *rotationUsageAbsenceProof) { p.ExpiresAt = p.ExpiresAt.Add(time.Nanosecond) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := a
			change(&changed)
			if rotationAbsenceEvidenceID(changed) == originalID {
				t.Fatal("changed lookup scope, result or time reused its evidence ID")
			}
		})
	}
	changed := a
	changed.EvidenceID = originalID
	if rotationAbsenceEvidenceID(changed) != originalID {
		t.Fatal("digest must exclude its own evidence ID")
	}
	changed.ObservedAt = changed.ObservedAt.In(time.FixedZone("other", 3600))
	changed.ExpiresAt = changed.ExpiresAt.In(time.FixedZone("other", 3600))
	if rotationAbsenceEvidenceID(changed) != originalID {
		t.Fatal("equivalent UTC instants changed canonical digest")
	}
}

func TestExpiryRotationLegacyPreviewCannotResumeAsJoinEligible(t *testing.T) {
	for _, version := range []*int{nil, new(int)} {
		original := ownerapi.ExpiryRotationPreview{PolicyVersion: version, Status: "ready", Digest: "old-frozen-digest", Candidates: []ownerapi.ExpiryRotationCandidate{{AccountId: uuid.New(), Decision: "eligible", UsageState: "never_used", DeliveryStatus: "join_candidate_pending_first_probe"}}}
		p, status := rotationNormalizeHistoricalPreview(original, "ready")
		if status != "needs_verification" || p.Status != "needs_verification" || p.Candidates[0].Decision != "excluded" || p.Candidates[0].DeliveryStatus != "blocked" || p.Candidates[0].Reason != "legacy_preview_requires_repreview" || p.Digest != original.Digest {
			t.Fatalf("old policy ready preview remained usable: %+v %s", p, status)
		}
		if original.Candidates[0].Decision != "eligible" || original.Candidates[0].DeliveryStatus != "join_candidate_pending_first_probe" {
			t.Fatal("normalization modified frozen input")
		}
	}
	version := rotationPreviewPolicyVersion
	current := ownerapi.ExpiryRotationPreview{PolicyVersion: &version, Status: "ready", Candidates: []ownerapi.ExpiryRotationCandidate{{Decision: "eligible", DeliveryStatus: "join_candidate_pending_first_probe"}}}
	p, status := rotationNormalizeHistoricalPreview(current, "ready")
	if status != "ready" || p.Candidates[0].Decision != "eligible" || p.Candidates[0].DeliveryStatus != "join_candidate_pending_first_probe" {
		t.Fatal("current version join candidate was blocked")
	}
}

func TestOfficialRotationSnapshotABRequiresExactTypedFacts(t *testing.T) {
	now := time.Now().UTC()
	active := now.Add(-time.Hour)
	paid, members, invites := 2, 1, 1
	facts := platform.SelectedWorkspaceFacts{Permission: "read", Result: platform.Result{
		Outcome: platform.OutcomeOperational, Completeness: platform.Complete, ObservedAt: now,
		ActiveUntil: &active, SeatLimit: &paid, MemberCount: &members, PendingInviteCount: &invites,
		SeatTypeCounts: map[string]int{"default": 0, "usage_based": 0, "automation": 0, "prolite": 1},
		Members:        []platform.Member{{Kind: "member", PlatformMemberID: "member-1", Identifier: "old@example.test", Status: "listed", Role: "member", SeatType: "prolite"}, {Kind: "pending_invite", Identifier: "new@example.test", Status: "pending", SeatType: "prolite"}},
	}}
	if _, ok := compareOfficialRotationSnapshots(facts, facts); !ok {
		t.Fatal("identical complete typed snapshots rejected")
	}
	changed := facts
	changed.Result.Members = append([]platform.Member{}, facts.Result.Members...)
	changed.Result.Members[1].SeatType = "default"
	if _, ok := compareOfficialRotationSnapshots(facts, changed); ok {
		t.Fatal("invitation seat-type drift accepted")
	}
	changed = facts
	changed.Result.SeatTypeCounts = map[string]int{"default": 1, "usage_based": 0, "automation": 0, "prolite": 0}
	if _, ok := compareOfficialRotationSnapshots(facts, changed); ok {
		t.Fatal("typed occupancy drift accepted")
	}
	changed = facts
	changed.Permission = "denied"
	if _, ok := compareOfficialRotationSnapshots(facts, changed); ok {
		t.Fatal("denied second read accepted")
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
	if !validRotationProof(proof, now, "mock_write_permission") {
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
		if validRotationProof(altered, now, "mock_write_permission") {
			t.Fatalf("accepted invalid proof %+v", altered)
		}
	}
}
func TestExpiryRotationRequiresExplicitCompatibleAssignments(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	p := ownerapi.ExpiryRotationPreview{Slots: []ownerapi.ExpiryRotationSlot{{PlatformMemberId: "slot-a", SeatType: "default", Decision: "replaceable"}, {PlatformMemberId: "slot-b", SeatType: "default", Decision: "replaceable"}}, Candidates: []ownerapi.ExpiryRotationCandidate{{AccountId: a, SeatType: "default", Decision: "eligible", DeliveryStatus: "join_candidate_pending_first_probe"}, {AccountId: b, SeatType: "default", Decision: "eligible", DeliveryStatus: "join_candidate_pending_first_probe"}}}
	choices := []ownerapi.ExpiryRotationAssignment{{PlatformMemberId: "slot-b", AccountId: b}, {PlatformMemberId: "slot-a", AccountId: a}}
	legacy := p
	legacy.Candidates = append([]ownerapi.ExpiryRotationCandidate{}, p.Candidates...)
	legacy.Candidates[0].DeliveryStatus = ""
	if _, ok := rotationAssignments(legacy, choices); ok {
		t.Fatal("legacy eligible candidate without delivery status mapped")
	}
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
	p.Candidates = append(p.Candidates, ownerapi.ExpiryRotationCandidate{AccountId: uuid.New(), SeatType: "default", Decision: "eligible", DeliveryStatus: "join_candidate_pending_first_probe"})
	if _, ok := rotationAssignments(p, choices); ok {
		t.Fatal("excess eligible candidates silently narrowed")
	}
}

func TestExpiryRotationDigestBindsExactScope(t *testing.T) {
	version := rotationPreviewPolicyVersion
	p := ownerapi.ExpiryRotationPreview{PolicyVersion: &version, DraftId: uuid.New(), DraftVersion: 1, WorkspaceId: uuid.New(), SourceRevisions: map[string]int64{"account": 1}, Members: []string{"old@example.test · id-1"}, Invitations: []string{"new@example.test · pending"}, Slots: []ownerapi.ExpiryRotationSlot{{Identifier: "old@example.test", PlatformMemberId: "id-1", SeatType: "default", Decision: "replaceable", Reason: "mock-unprotected"}}, Candidates: []ownerapi.ExpiryRotationCandidate{{AccountId: uuid.New(), Decision: "eligible", Reason: "invited", SeatType: "default"}}}
	digest := rotationDigest(p)
	p.PolicyVersion = nil
	if rotationDigest(p) == digest {
		t.Fatal("policy change reused preview digest")
	}
	p.PolicyVersion = &version
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
