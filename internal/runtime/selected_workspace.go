package runtime

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/identity"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

func (h *OwnerAuthHandler) GetSelectedWorkspaceVerification(w http.ResponseWriter, r *http.Request, workspaceID openapi_types.UUID, params ownerapi.GetSelectedWorkspaceVerificationParams) {
	if _, ok := h.authenticated(w, r, false); !ok {
		return
	}
	response, err := h.selectedWorkspace(r.Context(), workspaceID, params.MotherAccountId)
	if !h.selectedWorkspaceError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *OwnerAuthHandler) VerifySelectedWorkspace(w http.ResponseWriter, r *http.Request, workspaceID openapi_types.UUID, _ ownerapi.VerifySelectedWorkspaceParams) {
	if _, ok := h.authenticated(w, r, true); !ok {
		return
	}
	var request ownerapi.VerifySelectedWorkspaceJSONRequestBody
	if !decodeJSON(w, r, &request) || !bool(request.Confirmed) {
		writeProblem(w, r, 422, "selection_required", "Selection Required", "Confirm the selected mother and canonical workspace", 0)
		return
	}
	current, err := h.selectedWorkspace(r.Context(), workspaceID, request.MotherAccountId)
	if !h.selectedWorkspaceError(w, r, err) {
		return
	}
	if current.AccessStatus != "readable" {
		writeProblem(w, r, 403, "workspace_permission_denied", "Forbidden", "Verify access through another mother", 0)
		return
	}
	if h.selectedWorkspaceReader == nil {
		writeProblem(w, r, 501, "management_protocol_unavailable", "Unavailable", "No verified Workspace management protocol is configured", 0)
		return
	}
	var platformID string
	err = h.pool.QueryRow(r.Context(), `SELECT platform_workspace_id FROM tsw_workspaces WHERE id=$1`, workspaceID).Scan(&platformID)
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	facts, readErr := h.selectedWorkspaceReader.VerifySelectedWorkspace(ctx, request.MotherAccountId.String(), platformID)
	cancel()
	status, observedAt := "failed", time.Now().UTC()
	var activeUntil *time.Time
	var seats, members, invites *int
	permission, completeness := facts.Permission, string(facts.Result.Completeness)
	if permission != "manage" && permission != "read" && permission != "denied" {
		permission = "unknown"
	}
	if completeness != "complete" && completeness != "partial" {
		completeness = "unknown"
	}
	if readErr == nil {
		observedAt = facts.Result.ObservedAt
		if observedAt.IsZero() || observedAt.After(time.Now()) || observedAt.Before(time.Now().Add(-7*24*time.Hour)) {
			status, observedAt, permission, completeness = "failed", time.Now().UTC(), "unknown", "unknown"
		} else if permission == "denied" || facts.Result.Outcome == platform.OutcomeForbidden || facts.Result.Outcome == platform.OutcomeUnauthorized {
			status = "permission_denied"
		} else if permission == "manage" && facts.Result.Outcome == platform.OutcomeOperational && completeness == "complete" &&
			facts.Result.ActiveUntil != nil && facts.Result.SeatLimit != nil && facts.Result.MemberCount != nil && facts.Result.PendingInviteCount != nil &&
			*facts.Result.SeatLimit >= 0 && *facts.Result.MemberCount >= 0 && *facts.Result.PendingInviteCount >= 0 && validSelectedEntries(facts.Result.Members, *facts.Result.MemberCount, *facts.Result.PendingInviteCount) {
			status = "verified"
			activeUntil, seats, members, invites = facts.Result.ActiveUntil, facts.Result.SeatLimit, facts.Result.MemberCount, facts.Result.PendingInviteCount
		} else if facts.Result.Outcome == platform.OutcomeOperational {
			status = "partial"
			completeness = "partial"
		} else {
			permission = "unknown"
		}
	} else {
		permission, completeness = "unknown", "unknown"
	}
	// Recheck the discovery/session fence after the adapter returns. No permission
	// obtained on a different mother or a revoked session may publish facts.
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var access string
	err = tx.QueryRow(r.Context(), selectedWorkspaceAccessSQL+` FOR SHARE OF account,credential,discovery,session,visibility`, workspaceID, request.MotherAccountId).Scan(&access, &platformID)
	if err != nil || access != "readable" {
		if errors.Is(err, pgx.ErrNoRows) || err == nil {
			writeProblem(w, r, 409, "workspace_access_changed", "Conflict", "Discover and select the workspace again", 0)
		} else {
			h.workspaceFailure(w, r, err)
		}
		return
	}
	var verificationID int64
	err = tx.QueryRow(r.Context(), `INSERT INTO tsw_workspace_verifications(workspace_id,mother_account_id,source,outcome,permission,completeness,observed_at,expires_at,active_until,seat_limit,member_count,pending_invite_count)
		VALUES($1,$2,'injected_platform_reader',$3,$4,$5,$6,$6::timestamptz+interval '7 days',$7,$8,$9,$10) RETURNING id`,
		workspaceID, request.MotherAccountId, status, permission, completeness, observedAt, activeUntil, seats, members, invites).Scan(&verificationID)
	if err == nil && status == "verified" {
		for _, member := range facts.Result.Members {
			var normalized string
			var version uint16
			var fingerprint [32]byte
			normalized, version, fingerprint, err = identity.Fingerprint(h.keyRing, identity.WorkspaceEntry, member.Identifier)
			if err != nil {
				break
			}
			var platformMemberID, role *string
			if member.Kind == "member" {
				platformMemberID = &member.PlatformMemberID
			}
			if member.Role != "" {
				role = &member.Role
			}
			_, err = tx.Exec(r.Context(), `INSERT INTO tsw_workspace_verification_entries(verification_id,kind,identifier,identifier_hmac,identifier_key_version,status,role,platform_member_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, verificationID, member.Kind, normalized, fingerprint[:], version, member.Status, role, platformMemberID)
			if err != nil {
				break
			}
		}
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	response, err := h.selectedWorkspace(r.Context(), workspaceID, request.MotherAccountId)
	if !h.selectedWorkspaceError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// Only a current discovery run and a live session authorize this specific
// mother/workspace pair. Readability is never a management grant.
const selectedWorkspaceAccessSQL = `SELECT visibility.access_status,workspace.platform_workspace_id
	FROM tsw_mother_workspace_visibility visibility
	JOIN tsw_workspaces workspace ON workspace.id=visibility.workspace_id
	JOIN tsw_mother_accounts account ON account.id=visibility.mother_account_id AND account.status='active'
	JOIN tsw_mother_account_credentials credential ON credential.mother_account_id=account.id
	JOIN tsw_mother_discoveries discovery ON discovery.mother_account_id=account.id AND discovery.run_id=visibility.run_id AND discovery.secret_revision=credential.secret_revision AND discovery.status='discovered' AND discovery.observed_at>now()-interval '7 days'
	JOIN tsw_mother_personal_sessions session ON session.mother_account_id=account.id AND session.generation=discovery.session_generation AND session.secret_revision=credential.secret_revision AND session.expires_at>now()
	WHERE visibility.workspace_id=$1 AND visibility.mother_account_id=$2`

func (h *OwnerAuthHandler) selectedWorkspace(ctx context.Context, workspaceID, motherID uuid.UUID) (ownerapi.SelectedWorkspaceVerification, error) {
	response := ownerapi.SelectedWorkspaceVerification{WorkspaceId: workspaceID, MotherAccountId: motherID, Status: "pending", Permission: "unknown", Completeness: "unknown", Members: []ownerapi.SelectedWorkspaceMember{}}
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return response, err
	}
	defer tx.Rollback(ctx)
	var platformID string
	err = tx.QueryRow(ctx, `SELECT display_name FROM tsw_workspaces WHERE id=$1`, workspaceID).Scan(&response.WorkspaceName)
	if err != nil {
		return response, err
	}
	err = tx.QueryRow(ctx, selectedWorkspaceAccessSQL, workspaceID, motherID).Scan(&response.AccessStatus, &platformID)
	if err != nil {
		return response, err
	}
	if response.AccessStatus != "readable" {
		response.Status = "permission_denied"
		return response, tx.Commit(ctx)
	}
	// A missing management reader cannot promote historical mock evidence into
	// current management facts after a process restart or configuration change.
	if h.selectedWorkspaceReader == nil {
		return response, tx.Commit(ctx)
	}
	var id int64
	var source, status, permission, completeness string
	var observed, expires time.Time
	err = tx.QueryRow(ctx, `SELECT id,source,outcome,permission,completeness,observed_at,expires_at FROM tsw_workspace_verifications WHERE workspace_id=$1 AND mother_account_id=$2 ORDER BY id DESC LIMIT 1`, workspaceID, motherID).Scan(&id, &source, &status, &permission, &completeness, &observed, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return response, tx.Commit(ctx)
	}
	if err != nil {
		return response, err
	}
	response.Source, response.ObservedAt, response.ExpiresAt = &source, &observed, &expires
	response.Permission, response.Completeness = ownerapi.SelectedWorkspaceVerificationPermission(permission), ownerapi.SelectedWorkspaceVerificationCompleteness(completeness)
	response.Status = ownerapi.SelectedWorkspaceVerificationStatus(status)
	if !expires.After(time.Now()) {
		response.Status = "stale"
		return response, tx.Commit(ctx)
	}
	if status != "verified" {
		return response, tx.Commit(ctx)
	}
	err = tx.QueryRow(ctx, `SELECT active_until,seat_limit,member_count,pending_invite_count FROM tsw_workspace_verifications WHERE id=$1`, id).Scan(&response.ActiveUntil, &response.SeatLimit, &response.MemberCount, &response.PendingInviteCount)
	if err != nil {
		return response, err
	}
	rows, err := tx.Query(ctx, `SELECT entry.kind,entry.identifier,entry.status,entry.role,
		(SELECT CASE WHEN count(*)=1 THEN (array_agg(target.id))[1] END FROM tsw_target_accounts target WHERE target.identifier=entry.identifier)
		FROM tsw_workspace_verification_entries entry WHERE entry.verification_id=$1 ORDER BY entry.kind,entry.identifier`, id)
	if err != nil {
		return response, err
	}
	defer rows.Close()
	for rows.Next() {
		var member ownerapi.SelectedWorkspaceMember
		if err := rows.Scan(&member.Kind, &member.Identifier, &member.Status, &member.Role, &member.ChildAccountId); err != nil {
			return response, err
		}
		member.Source, member.ObservedAt = source, observed
		member.Completeness, member.Permission, member.LatestVerification = "complete", "manage", "verified"
		response.Members = append(response.Members, member)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return response, err
	}
	return response, tx.Commit(ctx)
}

func (h *OwnerAuthHandler) selectedWorkspaceError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 409, "workspace_selection_expired", "Selection Expired", "Discover and select the workspace again", 0)
	} else {
		h.workspaceFailure(w, r, err)
	}
	return false
}

func validSelectedEntries(members []platform.Member, memberCount, invitationCount int) bool {
	seen := make(map[string]bool, len(members))
	foundMembers, foundInvitations := 0, 0
	for _, member := range members {
		identifier := strings.ToLower(strings.TrimSpace(member.Identifier))
		key := member.Kind + ":" + identifier
		if seen[key] || identifier == "" || utf8.RuneCountInString(identifier) > 254 ||
			utf8.RuneCountInString(member.Status) < 1 || utf8.RuneCountInString(member.Status) > 64 || utf8.RuneCountInString(member.Role) > 64 ||
			!(member.Kind == "member" && member.PlatformMemberID != "" || member.Kind == "pending_invite" && member.PlatformMemberID == "") {
			return false
		}
		seen[key] = true
		if member.Kind == "member" {
			foundMembers++
		} else {
			foundInvitations++
		}
	}
	return foundMembers == memberCount && foundInvitations == invitationCount
}
