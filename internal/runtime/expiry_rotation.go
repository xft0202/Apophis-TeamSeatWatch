package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/audit"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

// rotationCapability is deliberately absent from OwnerAuthConfig. Only disposable
// integration fixtures install a mock directly on the handler. Production has no
// independently sourced write, typed-seat or per-person protection capability.
type rotationCapability interface {
	Evidence(context.Context, uuid.UUID, uuid.UUID, int64) (rotationEvidence, error)
}
type rotationProof struct {
	Source     string    `json:"source"`
	EvidenceID string    `json:"evidenceId"`
	ObservedAt time.Time `json:"observedAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}
type rotationVerdict struct {
	rotationProof
	SeatType string `json:"seatType"`
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}
type rotationEvidence struct {
	WorkspaceID        uuid.UUID                     `json:"workspaceId"`
	MotherID           uuid.UUID                     `json:"motherId"`
	VerificationID     int64                         `json:"verificationId"`
	Permission         rotationProof                 `json:"permission"`
	PermissionDecision string                        `json:"permissionDecision"`
	Counts             rotationProof                 `json:"counts"`
	SeatTypeCounts     map[string]int                `json:"seatTypeCounts"`
	Slots              map[string]rotationVerdict    `json:"slots"`      // exact platform member IDs
	Candidates         map[uuid.UUID]rotationVerdict `json:"candidates"` // exact selected account IDs
}

func rotationExpired(activeUntil, now time.Time) bool { return !activeUntil.After(now) }

func validRotationProof(p rotationProof, source string, now time.Time) bool {
	return p.Source == source && p.EvidenceID != "" && !p.ObservedAt.After(now) && !p.ObservedAt.Before(now.Add(-5*time.Minute)) && p.ExpiresAt.After(now) && p.ExpiresAt.After(p.ObservedAt) && !p.ExpiresAt.After(p.ObservedAt.Add(5*time.Minute))
}
func rotationHash(value any) string {
	b, _ := json.Marshal(value)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func rotationDigest(p ownerapi.ExpiryRotationPreview) string {
	p.Id = uuid.Nil
	p.Digest = ""
	p.Authorized = false
	p.AuthorizedAt = nil
	p.AuthorizedBy = nil
	p.RevokedAt = nil
	p.Assignments = []ownerapi.ExpiryRotationAssignment{}
	p.AuthorizationDigest = nil
	return rotationHash(p)
}

type rotationRow interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func (h *OwnerAuthHandler) rotationFacts(ctx context.Context, db rotationRow, owner uuid.UUID) (ownerapi.ExpiryRotationPreview, error) {
	p := ownerapi.ExpiryRotationPreview{Status: "facts_incomplete", Assignments: []ownerapi.ExpiryRotationAssignment{}, Slots: []ownerapi.ExpiryRotationSlot{}, Candidates: []ownerapi.ExpiryRotationCandidate{}, Members: []string{}, Invitations: []string{}, SeatTypeCounts: map[string]int{}, SourceRevisions: map[string]int64{}, Source: "selected_workspace_read"}
	var children []byte
	var run, generation uuid.UUID
	var revision int64
	err := db.QueryRow(ctx, `SELECT id,version,mother_account_id,mother_revision,workspace_id,verification_id,batch_id,batch_version,children,destination_id,destination_revision,visibility_run_id,session_generation
 FROM tsw_operation_selection_drafts WHERE owner_id=$1 AND step='complete'`, owner).Scan(&p.DraftId, &p.DraftVersion, &p.MotherAccountId, &revision, &p.WorkspaceId, &p.VerificationId, &p.BatchId, &p.BatchVersion, &children, &p.DestinationId, &p.DestinationRevision, &run, &generation)
	if err != nil {
		return p, err
	}
	p.SourceRevisions["mother"] = revision
	// Each connection is scoped to the selected canonical Workspace and current
	// Personal and Workspace token generations. Latest failed reads supersede success.
	var memberCount, inviteCount, seatLimit int
	var permission string
	err = db.QueryRow(ctx, `SELECT f.active_until,f.observed_at,f.expires_at,f.member_count,f.pending_invite_count,f.seat_limit,f.permission
 FROM tsw_mother_accounts a
 JOIN tsw_mother_account_credentials c ON c.mother_account_id=a.id AND c.secret_revision=$6
 JOIN tsw_mother_discoveries discovery ON discovery.mother_account_id=a.id AND discovery.run_id=$3 AND discovery.status='discovered' AND discovery.secret_revision=c.secret_revision AND discovery.session_generation=$4 AND discovery.observed_at>now()-interval '7 days'
 JOIN tsw_mother_personal_sessions s ON s.mother_account_id=a.id AND s.generation=$4 AND s.secret_revision=c.secret_revision AND s.expires_at>now()
 JOIN tsw_mother_workspace_visibility v ON v.mother_account_id=a.id AND v.workspace_id=$2 AND v.run_id=$3 AND v.access_status='readable'
 JOIN tsw_selected_workspace_tokens t ON t.workspace_id=$2 AND t.mother_account_id=a.id AND t.discovery_run_id=$3 AND t.session_generation=$4 AND t.secret_revision=c.secret_revision AND t.status='ready' AND t.expires_at>now()+interval '30 seconds'
 JOIN tsw_workspace_verifications f ON f.id=$5 AND f.workspace_id=$2 AND f.mother_account_id=a.id AND f.discovery_run_id=$3 AND f.session_generation=$4 AND f.secret_revision=c.secret_revision AND f.token_attempt=t.attempt AND f.token_exchange_id=t.exchange_id AND f.outcome='verified' AND f.completeness='complete' AND f.expires_at>now() AND f.observed_at<=now() AND f.observed_at>now()-interval '5 minutes'
 WHERE a.id=$1 AND a.status='active' AND f.id=(SELECT max(id) FROM tsw_workspace_verifications WHERE workspace_id=$2 AND mother_account_id=a.id AND discovery_run_id=$3 AND session_generation=$4 AND token_attempt=t.attempt AND token_exchange_id=t.exchange_id)`, p.MotherAccountId, p.WorkspaceId, run, generation, p.VerificationId, revision).Scan(&p.ActiveUntil, &p.ObservedAt, &p.ExpiresAt, &memberCount, &inviteCount, &seatLimit, &permission)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if !rotationExpired(p.ActiveUntil, time.Now()) {
		p.Status = "not_expired"
	}
	var batchCurrent, destinationCurrent bool
	err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tsw_standby_child_batches WHERE id=$1 AND version=$2),EXISTS(SELECT 1 FROM tsw_delivery_destinations WHERE id=$3 AND revision=$4 AND enabled AND test_connection='connected' AND test_target='connected' AND test_revision=revision)`, p.BatchId, p.BatchVersion, p.DestinationId, p.DestinationRevision).Scan(&batchCurrent, &destinationCurrent)
	if err != nil {
		return p, err
	}
	if !batchCurrent || !destinationCurrent {
		return p, nil
	}
	rows, err := db.Query(ctx, `SELECT kind,identifier,status,COALESCE(role,''),COALESCE(platform_member_id,'') FROM tsw_workspace_verification_entries WHERE verification_id=$1 ORDER BY kind,identifier`, p.VerificationId)
	if err != nil {
		return p, err
	}
	type entry struct{ identifier, status, role, id string }
	var members []entry
	invitations := map[string]bool{}
	memberIdentifiers := map[string]bool{}
	memberIDs := map[string]bool{}
	ambiguousMember := false
	for rows.Next() {
		var kind string
		var e entry
		if err = rows.Scan(&kind, &e.identifier, &e.status, &e.role, &e.id); err != nil {
			break
		}
		if kind == "member" {
			if e.id == "" || memberIDs[e.id] {
				ambiguousMember = true
				break
			}
			memberIDs[e.id] = true
			memberIdentifiers[strings.ToLower(e.identifier)] = true
			members = append(members, e)
			p.Members = append(p.Members, e.identifier+" · "+e.id+" · "+e.status+" · "+e.role)
		} else {
			invitations[strings.ToLower(e.identifier)] = e.status == "pending"
			p.Invitations = append(p.Invitations, e.identifier+" · "+e.status)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return p, err
	}
	if ambiguousMember || len(members) != memberCount || len(p.Invitations) != inviteCount || len(invitations) != inviteCount || seatLimit < memberCount || memberCount < 0 {
		return p, nil
	}
	var selected []ownerapi.OperationDraftChild
	if err = json.Unmarshal(children, &selected); err != nil {
		return p, err
	}
	if len(selected) == 0 {
		return p, nil
	}
	// No batch count is an eligibility signal. Re-read each exact selected account,
	// its membership version, credential version and global delivery history.
	type child struct {
		id                                uuid.UUID
		identifier                        string
		accountVersion, credentialVersion int64
		complete, delivered               bool
	}
	found := make([]child, 0, len(selected))
	seen := make(map[uuid.UUID]bool, len(selected))
	for _, chosen := range selected {
		if seen[chosen.AccountId] || chosen.AccountId == uuid.Nil || chosen.MembershipVersion < 1 {
			return p, nil
		}
		seen[chosen.AccountId] = true
		var c child
		c.id = chosen.AccountId
		err = db.QueryRow(ctx, `SELECT a.identifier,a.version,creds.version,(a.status='active' AND creds.material_status='complete' AND creds.materials_sealed),
   EXISTS(SELECT 1 FROM tsw_batch_memberships m JOIN tsw_oauth_assets asset ON asset.membership_id=m.id JOIN tsw_delivery_versions delivered ON delivered.oauth_asset_id=asset.id WHERE m.target_account_id=a.id)
   FROM tsw_standby_child_memberships membership JOIN tsw_target_accounts a ON a.id=membership.target_account_id JOIN tsw_target_credentials creds ON creds.target_account_id=a.id
   WHERE membership.target_account_id=$1 AND membership.batch_id=$2 AND membership.version=$3`, c.id, p.BatchId, chosen.MembershipVersion).Scan(&c.identifier, &c.accountVersion, &c.credentialVersion, &c.complete, &c.delivered)
		if errors.Is(err, pgx.ErrNoRows) {
			return p, nil
		}
		if err != nil {
			return p, err
		}
		var identityCount int
		if err = db.QueryRow(ctx, `SELECT count(*) FROM tsw_target_accounts WHERE identifier=$1`, c.identifier).Scan(&identityCount); err != nil {
			return p, err
		}
		if identityCount != 1 {
			return p, nil
		}
		p.SourceRevisions["account:"+c.id.String()] = c.accountVersion
		p.SourceRevisions["credential:"+c.id.String()] = c.credentialVersion
		p.SourceRevisions["membership:"+c.id.String()] = chosen.MembershipVersion
		found = append(found, c)
	}
	pending := func() {
		for _, e := range members {
			p.Slots = append(p.Slots, ownerapi.ExpiryRotationSlot{Identifier: e.identifier, PlatformMemberId: e.id, Decision: "needs_verification", Reason: "protection_proof_missing"})
		}
		for _, c := range found {
			reason := "eligibility_proof_missing"
			if !invitations[strings.ToLower(c.identifier)] {
				reason = "invitation_required"
			}
			p.Candidates = append(p.Candidates, ownerapi.ExpiryRotationCandidate{AccountId: c.id, Identifier: c.identifier, Decision: "excluded", Reason: reason})
		}
	}
	if h.rotationCapability == nil {
		pending()
		if p.Status != "not_expired" {
			p.Status = "pending_permission"
		}
		return p, nil
	}
	ev, evidenceErr := h.rotationCapability.Evidence(ctx, p.WorkspaceId, p.MotherAccountId, p.VerificationId)
	if evidenceErr != nil {
		pending()
		return p, nil
	} // a failed protection/permission lookup is never eligible
	now := time.Now()
	if ev.WorkspaceID != p.WorkspaceId || ev.MotherID != p.MotherAccountId || ev.VerificationID != p.VerificationId || !validRotationProof(ev.Permission, "mock_write_permission", now) || ev.PermissionDecision != "manage" {
		pending()
		if p.Status != "not_expired" {
			p.Status = "pending_permission"
		}
		return p, nil
	}
	if !validRotationProof(ev.Counts, "mock_seat_type_counts", now) || len(ev.SeatTypeCounts) == 0 {
		pending()
		return p, nil
	}
	total := 0
	for kind, count := range ev.SeatTypeCounts {
		if kind == "" || count < 0 {
			pending()
			return p, nil
		}
		total += count
	}
	if total < memberCount {
		pending()
		return p, nil
	}
	p.SeatTypeCounts = ev.SeatTypeCounts
	p.Source = "mock_capability"
	p.EvidenceFingerprint = rotationHash(ev)
	ready := true
	replaceable, eligible := 0, 0
	observedSeatTypes := make(map[string]int)
	for _, e := range members {
		verdict, ok := ev.Slots[e.id]
		slot := ownerapi.ExpiryRotationSlot{Identifier: e.identifier, PlatformMemberId: e.id, Decision: "needs_verification", Reason: "protection_proof_missing"}
		if ok && validRotationProof(verdict.rotationProof, "mock_member_protection", now) && verdict.Reason != "" && verdict.SeatType != "" && (verdict.Decision == "replaceable" || verdict.Decision == "retained") {
			slot.SeatType = verdict.SeatType
			slot.Decision = ownerapi.ExpiryRotationSlotDecision(verdict.Decision)
			slot.Reason = verdict.Reason
			if slot.Decision == "replaceable" {
				if verdict.SeatType != "default" {
					slot.Decision = "needs_verification"
					slot.Reason = "seat_type_unverified"
				}
			}
		} else {
			ready = false
		}
		var delivered bool
		err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tsw_batch_memberships membership JOIN tsw_batches batch ON batch.id=membership.batch_id JOIN tsw_mother_workspace_bindings binding ON binding.id=batch.binding_id JOIN tsw_oauth_assets asset ON asset.membership_id=membership.id JOIN tsw_delivery_versions delivery ON delivery.oauth_asset_id=asset.id WHERE binding.workspace_id=$1 AND membership.platform_member_id=$2)`, p.WorkspaceId, e.id).Scan(&delivered)
		if err != nil {
			return p, err
		}
		if delivered {
			slot.Decision = "retained"
			slot.Reason = "global_delivery_protected"
			if verdict.SeatType == "" {
				ready = false
			}
		}
		if slot.Decision == "needs_verification" {
			ready = false
		}
		if slot.Decision == "replaceable" {
			replaceable++
		}
		if slot.SeatType != "" {
			observedSeatTypes[slot.SeatType]++
		}
		p.Slots = append(p.Slots, slot)
	}
	for _, c := range found {
		verdict, ok := ev.Candidates[c.id]
		candidate := ownerapi.ExpiryRotationCandidate{AccountId: c.id, Identifier: c.identifier, Decision: "excluded", Reason: "eligibility_proof_missing"}
		if ok && validRotationProof(verdict.rotationProof, "mock_candidate_protection", now) && verdict.Reason != "" && verdict.SeatType != "" && (verdict.Decision == "eligible" || verdict.Decision == "excluded") {
			candidate.Decision = ownerapi.ExpiryRotationCandidateDecision(verdict.Decision)
			candidate.Reason = verdict.Reason
			candidate.SeatType = verdict.SeatType
		} else {
			ready = false
		}
		if !c.complete {
			candidate.Decision = "excluded"
			candidate.Reason = "material_changed"
			ready = false
		}
		if c.delivered {
			candidate.Decision = "excluded"
			candidate.Reason = "global_delivery_protected"
		}
		if !invitations[strings.ToLower(c.identifier)] {
			candidate.Decision = "excluded"
			candidate.Reason = "invitation_required"
		}
		if memberIdentifiers[strings.ToLower(c.identifier)] {
			candidate.Decision = "excluded"
			candidate.Reason = "already_member"
		}
		if candidate.SeatType != "default" {
			candidate.Decision = "excluded"
			candidate.Reason = "premium_seat_unverified"
		}
		if candidate.Decision == "eligible" {
			eligible++
		}
		p.Candidates = append(p.Candidates, candidate)
	}
	for seatType, occupied := range observedSeatTypes {
		if p.SeatTypeCounts[seatType] < occupied {
			ready = false
		}
	}
	if p.Status == "not_expired" {
		return p, nil
	}
	if ready && replaceable > 0 && eligible == replaceable {
		p.Status = "ready"
	} else {
		p.Status = "needs_verification"
	}
	if ev.Permission.ExpiresAt.Before(p.ExpiresAt) {
		p.ExpiresAt = ev.Permission.ExpiresAt
	}
	if ev.Counts.ExpiresAt.Before(p.ExpiresAt) {
		p.ExpiresAt = ev.Counts.ExpiresAt
	}
	for _, v := range ev.Slots {
		if v.ExpiresAt.Before(p.ExpiresAt) {
			p.ExpiresAt = v.ExpiresAt
		}
	}
	for _, v := range ev.Candidates {
		if v.ExpiresAt.Before(p.ExpiresAt) {
			p.ExpiresAt = v.ExpiresAt
		}
	}
	// Scope the preview lifetime independently of Ticket07's seven-day read TTL.
	if limit := now.Add(5 * time.Minute); p.ExpiresAt.After(limit) {
		p.ExpiresAt = limit
	}
	return p, nil
}

func (h *OwnerAuthHandler) PreviewExpiryRotation(w http.ResponseWriter, r *http.Request, _ ownerapi.PreviewExpiryRotationParams) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	tx, err := h.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	p, err := h.rotationFacts(r.Context(), tx, uuid.MustParse(owner.OwnerID))
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 409, "draft_incomplete", "Incomplete", "Complete the selection wizard first", 0)
		return
	}
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	if p.DraftId == uuid.Nil {
		writeProblem(w, r, 409, "draft_incomplete", "Incomplete", "Complete the selection wizard first", 0)
		return
	}
	// A pending preview is visible but cannot be confirmed. Never default missing facts to zero.
	if p.ExpiresAt.IsZero() {
		p.ExpiresAt = time.Now().Add(time.Minute)
	}
	p.Digest = rotationDigest(p)
	facts, _ := json.Marshal(p)
	err = tx.QueryRow(r.Context(), `INSERT INTO tsw_expiry_rotation_previews(owner_id,draft_id,draft_version,workspace_id,verification_id,facts,digest,status,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`, owner.OwnerID, p.DraftId, p.DraftVersion, p.WorkspaceId, p.VerificationId, facts, p.Digest, p.Status, p.ExpiresAt).Scan(&p.Id)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	writeJSON(w, 200, p)
}
func rotationStored(ctx context.Context, tx pgx.Tx, owner uuid.UUID, id uuid.UUID) (ownerapi.ExpiryRotationPreview, uuid.UUID, string, error) {
	var p ownerapi.ExpiryRotationPreview
	var raw []byte
	var key *uuid.UUID
	var status string
	var assignments []byte
	err := tx.QueryRow(ctx, `SELECT facts,status,idempotency_key,authorized_by,authorized_at,revoked_at,assignments,authorization_digest FROM tsw_expiry_rotation_previews WHERE id=$1 AND owner_id=$2 FOR UPDATE`, id, owner).Scan(&raw, &status, &key, &p.AuthorizedBy, &p.AuthorizedAt, &p.RevokedAt, &assignments, &p.AuthorizationDigest)
	if err != nil {
		return p, uuid.Nil, "", err
	}
	authorizedBy, authorizedAt, revokedAt, authorizedDigest := p.AuthorizedBy, p.AuthorizedAt, p.RevokedAt, p.AuthorizationDigest
	if err = json.Unmarshal(raw, &p); err != nil {
		return p, uuid.Nil, "", err
	}
	p.Id = id
	p.Status = ownerapi.ExpiryRotationPreviewStatus(status)
	p.AuthorizedBy = authorizedBy
	p.AuthorizedAt = authorizedAt
	p.RevokedAt = revokedAt
	p.AuthorizationDigest = authorizedDigest
	if err = json.Unmarshal(assignments, &p.Assignments); err != nil {
		return p, uuid.Nil, "", err
	}
	p.Authorized = authorizedAt != nil && revokedAt == nil
	if key != nil {
		return p, *key, status, nil
	}
	return p, uuid.Nil, status, nil
}
func (h *OwnerAuthHandler) GetLatestExpiryRotationPreview(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.authenticated(w, r, false)
	if !ok {
		return
	}
	var id uuid.UUID
	err := h.pool.QueryRow(r.Context(), `SELECT id FROM tsw_expiry_rotation_previews WHERE owner_id=$1 ORDER BY created_at DESC,id DESC LIMIT 1`, owner.OwnerID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "preview_not_found", "Not Found", "No preview exists", 0)
		return
	}
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	h.GetExpiryRotationPreview(w, r, id)
}

func (h *OwnerAuthHandler) GetExpiryRotationPreview(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	owner, ok := h.authenticated(w, r, false)
	if !ok {
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	p, _, _, err := rotationStored(r.Context(), tx, uuid.MustParse(owner.OwnerID), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "preview_not_found", "Not Found", "Preview not found", 0)
		return
	}
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	writeJSON(w, 200, p)
}

// An assignment is not inferred from roster order. Every replaceable original
// slot must have one different explicitly chosen eligible candidate of the same
// independently evidenced seat type; excess candidates require a new draft.
func rotationAssignments(preview ownerapi.ExpiryRotationPreview, requested []ownerapi.ExpiryRotationAssignment) ([]ownerapi.ExpiryRotationAssignment, bool) {
	slots := map[string]string{}
	candidates := map[uuid.UUID]string{}
	for _, s := range preview.Slots {
		if s.Decision == "replaceable" {
			slots[s.PlatformMemberId] = s.SeatType
		}
	}
	for _, c := range preview.Candidates {
		if c.Decision == "eligible" {
			candidates[c.AccountId] = c.SeatType
		}
	}
	if len(slots) == 0 || len(requested) != len(slots) || len(candidates) != len(slots) {
		return nil, false
	}
	used := map[uuid.UUID]bool{}
	assigned := map[string]bool{}
	for _, a := range requested {
		seat, exists := slots[a.PlatformMemberId]
		typeOfCandidate, eligible := candidates[a.AccountId]
		if !exists || !eligible || seat != typeOfCandidate || used[a.AccountId] || assigned[a.PlatformMemberId] {
			return nil, false
		}
		used[a.AccountId] = true
		assigned[a.PlatformMemberId] = true
	}
	assignments := append([]ownerapi.ExpiryRotationAssignment{}, requested...)
	sort.Slice(assignments, func(i, j int) bool { return assignments[i].PlatformMemberId < assignments[j].PlatformMemberId })
	return assignments, true
}

func (h *OwnerAuthHandler) ConfirmExpiryRotation(w http.ResponseWriter, r *http.Request, id uuid.UUID, _ ownerapi.ConfirmExpiryRotationParams) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	var input ownerapi.ExpiryRotationConfirmation
	if !decodeJSON(w, r, &input) || !input.Confirmed || input.IdempotencyKey == uuid.Nil || len(input.Digest) != 64 || len(input.Assignments) == 0 || len(input.Assignments) > 1000 {
		writeProblem(w, r, 422, "confirmation_required", "Confirmation Required", "Confirm the exact preview with a unique key", 0)
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	oid := uuid.MustParse(owner.OwnerID)
	p, key, status, err := rotationStored(r.Context(), tx, oid, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "preview_not_found", "Not Found", "Preview not found", 0)
		return
	}
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	if status == "authorized" && key == input.IdempotencyKey && p.Digest == input.Digest {
		requested, valid := rotationAssignments(p, input.Assignments)
		if valid && rotationHash(requested) == rotationHash(p.Assignments) {
			writeJSON(w, 200, p)
			return
		}
		writeProblem(w, r, 409, "idempotency_conflict", "Conflict", "The confirmation key belongs to different assignments", 0)
		return
	}
	if status != "ready" {
		writeProblem(w, r, 409, string(p.Status), "Not Authorizable", "No executable authorization was created", 0)
		return
	}
	assignments, valid := rotationAssignments(p, input.Assignments)
	if !valid {
		writeProblem(w, r, 409, "assignment_incomplete", "Incomplete Mapping", "Choose one distinct eligible candidate per replaceable slot; revise the draft for excess or mismatched candidates", 0)
		return
	}
	if p.Digest != input.Digest || !time.Now().Before(p.ExpiresAt) {
		writeProblem(w, r, 409, "preview_stale", "Stale Preview", "Re-preview changed or expired facts", 0)
		return
	}
	// Lock draft until commit, then verify the exact facts, scope, roster, verdicts,
	// batch/destination revisions, permission and protection again. Any drift blocks.
	var draftVersion int64
	err = tx.QueryRow(r.Context(), `SELECT version FROM tsw_operation_selection_drafts WHERE id=$1 AND owner_id=$2 FOR SHARE`, p.DraftId, oid).Scan(&draftVersion)
	if err != nil || draftVersion != p.DraftVersion {
		writeProblem(w, r, 409, "preview_stale", "Stale Preview", "Selection changed", 0)
		return
	}
	fresh, err := h.rotationFacts(r.Context(), tx, oid)
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	fresh.ExpiresAt = p.ExpiresAt // a shorter independent proof still fails the fingerprint and freshness test
	if fresh.Status != "ready" || fresh.EvidenceFingerprint != p.EvidenceFingerprint || rotationDigest(fresh) != p.Digest {
		writeProblem(w, r, 409, "preview_stale", "Stale Preview", "Evidence changed; request a new preview", 0)
		return
	}
	assignmentJSON, _ := json.Marshal(assignments)
	binding := rotationHash(struct {
		Preview     string
		Assignments []ownerapi.ExpiryRotationAssignment
	}{p.Digest, assignments})
	_, err = tx.Exec(r.Context(), `UPDATE tsw_expiry_rotation_previews SET status='authorized',idempotency_key=$2,authorized_by=$3,authorized_session=$4,authorized_at=now(),assignments=$5,authorization_digest=$6 WHERE id=$1 AND status='ready'`, id, input.IdempotencyKey, oid, owner.SessionID, assignmentJSON, binding)
	if err == nil {
		_, err = audit.Write(r.Context(), tx, audit.Event{Type: audit.ExpiryRotationAuthorized, Actor: audit.ActorOwner, OwnerID: owner.OwnerID, RetentionScopeID: p.WorkspaceId.String(), EntityType: "expiry_rotation_preview", EntityID: id.String(), Outcome: audit.OutcomeSucceeded, CorrelationID: correlation(r), Details: audit.ExpiryRotationDetails{Digest: binding, Action: "authorized"}, IdempotencyKey: id.String() + ":authorized"})
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	now := time.Now().UTC()
	p.Status = "authorized"
	p.Assignments = assignments
	p.AuthorizationDigest = &binding
	p.Authorized = true
	p.AuthorizedAt = &now
	p.AuthorizedBy = &oid
	writeJSON(w, 200, p)
}
func (h *OwnerAuthHandler) RevokeExpiryRotation(w http.ResponseWriter, r *http.Request, id uuid.UUID, _ ownerapi.RevokeExpiryRotationParams) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	oid := uuid.MustParse(owner.OwnerID)
	p, _, status, err := rotationStored(r.Context(), tx, oid, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "preview_not_found", "Not Found", "Preview not found", 0)
		return
	}
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	if status == "revoked" {
		writeJSON(w, 200, p)
		return
	}
	if status != "authorized" {
		writeProblem(w, r, 409, "not_authorized", "Not Authorized", "Only an authorization may be revoked", 0)
		return
	}
	err = tx.QueryRow(r.Context(), `UPDATE tsw_expiry_rotation_previews SET status='revoked',revoked_by=$2,revoked_at=now() WHERE id=$1 RETURNING revoked_at`, id, oid).Scan(&p.RevokedAt)
	if err == nil {
		_, err = audit.Write(r.Context(), tx, audit.Event{Type: audit.ExpiryRotationRevoked, Actor: audit.ActorOwner, OwnerID: owner.OwnerID, RetentionScopeID: p.WorkspaceId.String(), EntityType: "expiry_rotation_preview", EntityID: id.String(), Outcome: audit.OutcomeSucceeded, CorrelationID: correlation(r), Details: audit.ExpiryRotationDetails{Digest: *p.AuthorizationDigest, Action: "revoked"}, IdempotencyKey: id.String() + ":revoked"})
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	p.Status = "revoked"
	p.Authorized = false
	writeJSON(w, 200, p)
}
