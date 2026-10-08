package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

const draftColumns = `id,version,step,mother_account_id,mother_revision,workspace_id,visibility_run_id,session_generation,verification_id,batch_id,batch_version,children,execution_batch_id,planned_at,updated_at,rotation_from_batch_id,(SELECT source_standby_batch_id FROM tsw_batches WHERE id=rotation_from_batch_id)`

func readOperationDraft(row pgx.Row) (ownerapi.OperationDraft, error) {
	var d ownerapi.OperationDraft
	var children []byte
	err := row.Scan(&d.Id, &d.Version, &d.Step, &d.MotherAccountId, &d.MotherRevision, &d.WorkspaceId, &d.VisibilityRunId, &d.SessionGeneration, &d.VerificationId, &d.BatchId, &d.BatchVersion, &children, &d.ExecutionBatchId, &d.PlannedAt, &d.UpdatedAt, &d.PreviousBatchId, &d.ExcludedSourceBatchId)
	if err == nil {
		err = json.Unmarshal(children, &d.Children)
	}
	return d, err
}

func (h *OwnerAuthHandler) draftResponse(w http.ResponseWriter, r *http.Request, ownerID uuid.UUID) {
	d, err := readOperationDraft(h.pool.QueryRow(r.Context(), `SELECT `+draftColumns+` FROM tsw_operation_selection_drafts WHERE owner_id=$1`, ownerID))
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "draft_not_started", "Not Found", "Start an operation selection draft", 0)
		return
	}
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	err = checkOperationDraft(r, h.pool, &d)
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	writeJSON(w, 200, d)
}

type operationDraftReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func checkOperationDraft(r *http.Request, reader operationDraftReader, d *ownerapi.OperationDraft) error {
	var err error
	// Status is evaluated afresh, but never changes a frozen selection on read.
	if d.MotherAccountId != nil {
		err = reader.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM tsw_mother_accounts a JOIN tsw_mother_account_credentials c ON c.mother_account_id=a.id WHERE a.id=$1 AND a.status='active' AND c.secret_revision=$2)`, d.MotherAccountId, d.MotherRevision).Scan(&d.MotherCurrent)
	}
	if err == nil && d.WorkspaceId != nil && d.MotherCurrent {
		err = reader.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM tsw_mother_workspace_visibility v
   JOIN tsw_mother_discoveries discovery ON discovery.mother_account_id=v.mother_account_id AND discovery.run_id=v.run_id AND discovery.status='discovered' AND discovery.observed_at>now()-interval '7 days'
   JOIN tsw_mother_account_credentials c ON c.mother_account_id=v.mother_account_id AND c.secret_revision=discovery.secret_revision
   JOIN tsw_mother_personal_sessions s ON s.mother_account_id=v.mother_account_id AND s.generation=discovery.session_generation AND s.expires_at>now()
   JOIN tsw_selected_workspace_tokens t ON t.workspace_id=v.workspace_id AND t.mother_account_id=v.mother_account_id AND t.discovery_run_id=v.run_id AND t.session_generation=s.generation AND t.secret_revision=c.secret_revision AND t.status='ready' AND t.expires_at>now()
   JOIN tsw_workspace_verifications f ON f.id=$5 AND f.workspace_id=v.workspace_id AND f.mother_account_id=v.mother_account_id AND f.discovery_run_id=v.run_id AND f.session_generation=s.generation AND f.secret_revision=c.secret_revision AND f.token_attempt=t.attempt AND f.token_exchange_id=t.exchange_id AND f.outcome='verified' AND f.completeness='complete' AND f.permission='read' AND f.expires_at>now()
   WHERE v.workspace_id=$1 AND v.mother_account_id=$2 AND v.run_id=$3 AND s.generation=$4 AND v.access_status='readable'
   AND f.id=(SELECT max(id) FROM tsw_workspace_verifications WHERE workspace_id=v.workspace_id AND mother_account_id=v.mother_account_id AND discovery_run_id=v.run_id AND session_generation=s.generation AND token_attempt=t.attempt AND token_exchange_id=t.exchange_id))`, d.WorkspaceId, d.MotherAccountId, d.VisibilityRunId, d.SessionGeneration, d.VerificationId).Scan(&d.WorkspaceCurrent)
	}
	if err == nil && d.BatchId != nil && d.WorkspaceCurrent {
		err = reader.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM tsw_standby_child_batches b WHERE b.id=$1 AND b.deleted_at IS NULL AND b.version=$2 AND (SELECT count(*) FROM jsonb_to_recordset($3::jsonb) AS chosen("accountId" uuid,"membershipVersion" bigint) JOIN tsw_standby_child_memberships m ON m.target_account_id=chosen."accountId" AND m.batch_id=b.id AND m.version=chosen."membershipVersion" JOIN tsw_target_accounts a ON a.id=m.target_account_id JOIN tsw_target_credentials c ON c.target_account_id=a.id WHERE a.status='active' AND c.material_status='complete' AND c.materials_sealed)=jsonb_array_length($3::jsonb))`, d.BatchId, d.BatchVersion, mustDraftJSON(d.Children)).Scan(&d.BatchCurrent)
	}
	return err
}

func mustDraftJSON(children []ownerapi.OperationDraftChild) []byte {
	result, _ := json.Marshal(children)
	return result
}

func (h *OwnerAuthHandler) GetOperationDraft(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.authenticated(w, r, false)
	if ok {
		h.draftResponse(w, r, uuid.MustParse(owner.OwnerID))
	}
}
func (h *OwnerAuthHandler) StartOperationDraft(w http.ResponseWriter, r *http.Request, _ ownerapi.StartOperationDraftParams) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	id := uuid.MustParse(owner.OwnerID)
	_, err := h.pool.Exec(r.Context(), `INSERT INTO tsw_operation_selection_drafts(owner_id) VALUES($1) ON CONFLICT(owner_id) DO NOTHING`, id)
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	h.draftResponse(w, r, id)
}

var errDraftConflict = errors.New("draft changed")
var errDraftMaterialIncomplete = errors.New("child material incomplete")

func (h *OwnerAuthHandler) ChangeOperationDraft(w http.ResponseWriter, r *http.Request, _ ownerapi.ChangeOperationDraftParams) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	var change ownerapi.OperationDraftChange
	// A 10000-child exact ID/version set can exceed the ordinary 512 KiB Owner limit.
	if !decodeStandbyChange(w, r, &change) || change.ExpectedVersion < 1 {
		writeProblem(w, r, 422, "invalid_draft", "Invalid Draft", "Choose one explicit step", 0)
		return
	}
	id := uuid.MustParse(owner.OwnerID)
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	d, err := readOperationDraft(tx.QueryRow(r.Context(), `SELECT `+draftColumns+` FROM tsw_operation_selection_drafts WHERE owner_id=$1 FOR UPDATE`, id))
	if err == nil && d.Version != change.ExpectedVersion {
		err = errDraftConflict
	}
	if err != nil {
		h.draftChangeError(w, r, err)
		return
	}
	if d.ExecutionBatchId != nil {
		var status string
		if err = tx.QueryRow(r.Context(), `SELECT status FROM tsw_batches WHERE id=$1 FOR UPDATE`, d.ExecutionBatchId).Scan(&status); err != nil {
			h.draftChangeError(w, r, err)
			return
		}
		if status != "planned" && status != "draft" {
			writeProblem(w, r, 409, "operation_frozen", "Conflict", "The running operation selection is frozen", 0)
			return
		}
	}
	if change.Choice == "children" && d.ExcludedSourceBatchId != nil && change.BatchId != nil && *d.ExcludedSourceBatchId == *change.BatchId {
		writeProblem(w, r, 409, "rotation_same_source_batch", "Conflict", "Choose a different source batch for the next rotation", 0)
		return
	}
	// Subsequent choices must use the exact currently validated mother/space.
	// A read-only verification permits selection, never write authorization.
	if change.Choice == "children" {
		var current bool
		if d.WorkspaceId != nil {
			err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM tsw_mother_workspace_visibility v
			 JOIN tsw_mother_accounts a ON a.id=v.mother_account_id AND a.status='active'
			 JOIN tsw_mother_account_credentials c ON c.mother_account_id=a.id AND c.secret_revision=$6
			 JOIN tsw_mother_discoveries discovery ON discovery.mother_account_id=a.id AND discovery.run_id=v.run_id AND discovery.secret_revision=c.secret_revision AND discovery.status='discovered' AND discovery.observed_at>now()-interval '7 days'
			 JOIN tsw_mother_personal_sessions s ON s.mother_account_id=a.id AND s.generation=discovery.session_generation AND s.expires_at>now()
			 JOIN tsw_selected_workspace_tokens t ON t.workspace_id=v.workspace_id AND t.mother_account_id=a.id AND t.discovery_run_id=v.run_id AND t.session_generation=s.generation AND t.secret_revision=c.secret_revision AND t.status='ready' AND t.expires_at>now()
			 JOIN tsw_workspace_verifications f ON f.id=$5 AND f.workspace_id=v.workspace_id AND f.mother_account_id=a.id AND f.discovery_run_id=v.run_id AND f.session_generation=s.generation AND f.secret_revision=c.secret_revision AND f.token_attempt=t.attempt AND f.token_exchange_id=t.exchange_id AND f.outcome='verified' AND f.completeness='complete' AND f.permission='read' AND f.expires_at>now()
			 WHERE v.workspace_id=$1 AND v.mother_account_id=$2 AND v.run_id=$3 AND s.generation=$4 AND v.access_status='readable'
			 AND f.id=(SELECT max(id) FROM tsw_workspace_verifications WHERE workspace_id=v.workspace_id AND mother_account_id=a.id AND discovery_run_id=v.run_id AND session_generation=s.generation AND token_attempt=t.attempt AND token_exchange_id=t.exchange_id))`, d.WorkspaceId, d.MotherAccountId, d.VisibilityRunId, d.SessionGeneration, d.VerificationId, d.MotherRevision).Scan(&current)
		}
		if err != nil {
			h.draftChangeError(w, r, err)
			return
		}
		if !current {
			h.draftChangeError(w, r, errDraftConflict)
			return
		}
	}
	// The input shape is exclusive: a stale field cannot silently select a different object.
	valid := false
	switch change.Choice {
	case "mother":
		if change.MotherAccountId != nil && *change.MotherAccountId != uuid.Nil && change.WorkspaceId == nil && change.BatchId == nil && change.Children == nil && change.BatchVersion == nil {
			var rev int64
			err = tx.QueryRow(r.Context(), `SELECT c.secret_revision FROM tsw_mother_accounts a JOIN tsw_mother_account_credentials c ON c.mother_account_id=a.id WHERE a.id=$1 AND a.status='active' FOR SHARE OF a,c`, change.MotherAccountId).Scan(&rev)
			if err == nil {
				_, err = tx.Exec(r.Context(), `UPDATE tsw_operation_selection_drafts SET rotation_from_batch_id=CASE WHEN EXISTS(SELECT 1 FROM tsw_batches prior JOIN tsw_mother_workspace_bindings b ON b.id=prior.binding_id WHERE prior.id=rotation_from_batch_id AND b.mother_account_id=$2) THEN rotation_from_batch_id ELSE NULL END,mother_account_id=$2,mother_revision=$3,workspace_id=NULL,visibility_run_id=NULL,session_generation=NULL,verification_id=NULL,batch_id=NULL,batch_version=NULL,children='[]',step='workspace' WHERE owner_id=$1`, id, change.MotherAccountId, rev)
				valid = true
			}
		}
	case "workspace":
		if d.MotherAccountId != nil && change.WorkspaceId != nil && *change.WorkspaceId != uuid.Nil && change.MotherAccountId == nil && change.BatchId == nil && change.Children == nil && change.BatchVersion == nil {
			var run, generation uuid.UUID
			var verification int64
			// A readable discovery is not write authority. Only a fresh complete read
			// can be captured; no selection authorizes invitation or seat changes.
			err = tx.QueryRow(r.Context(), `SELECT v.run_id,s.generation,f.id FROM tsw_mother_workspace_visibility v
    JOIN tsw_mother_accounts a ON a.id=v.mother_account_id AND a.status='active'
    JOIN tsw_mother_discoveries discovery ON discovery.mother_account_id=v.mother_account_id AND discovery.run_id=v.run_id AND discovery.status='discovered' AND discovery.observed_at>now()-interval '7 days'
    JOIN tsw_mother_account_credentials c ON c.mother_account_id=v.mother_account_id AND c.secret_revision=discovery.secret_revision AND c.secret_revision=$3
    JOIN tsw_mother_personal_sessions s ON s.mother_account_id=v.mother_account_id AND s.generation=discovery.session_generation AND s.expires_at>now()
    JOIN tsw_selected_workspace_tokens t ON t.workspace_id=v.workspace_id AND t.mother_account_id=v.mother_account_id AND t.discovery_run_id=v.run_id AND t.session_generation=s.generation AND t.secret_revision=c.secret_revision AND t.status='ready' AND t.expires_at>now()
    JOIN LATERAL (SELECT id FROM tsw_workspace_verifications WHERE workspace_id=v.workspace_id AND mother_account_id=v.mother_account_id AND discovery_run_id=v.run_id AND session_generation=s.generation AND secret_revision=c.secret_revision AND token_attempt=t.attempt AND token_exchange_id=t.exchange_id ORDER BY id DESC LIMIT 1) f ON true
    JOIN tsw_workspace_verifications evidence ON evidence.id=f.id AND evidence.outcome='verified' AND evidence.completeness='complete' AND evidence.permission='read' AND evidence.expires_at>now()
    WHERE v.workspace_id=$1 AND v.mother_account_id=$2 AND v.access_status='readable' FOR SHARE OF v,discovery,c,s,t,evidence`, change.WorkspaceId, d.MotherAccountId, d.MotherRevision).Scan(&run, &generation, &verification)
			if err == nil {
				_, err = tx.Exec(r.Context(), `UPDATE tsw_operation_selection_drafts SET rotation_from_batch_id=CASE WHEN EXISTS(SELECT 1 FROM tsw_batches prior JOIN tsw_mother_workspace_bindings b ON b.id=prior.binding_id WHERE prior.id=rotation_from_batch_id AND b.workspace_id=$2) THEN rotation_from_batch_id ELSE NULL END,workspace_id=$2,visibility_run_id=$3,session_generation=$4,verification_id=$5,batch_id=NULL,batch_version=NULL,children='[]',step='children' WHERE owner_id=$1`, id, change.WorkspaceId, run, generation, verification)
				valid = true
			}
		}
	case "children":
		if d.WorkspaceId != nil && change.BatchId != nil && change.BatchVersion != nil && *change.BatchVersion > 0 && change.Children != nil && len(*change.Children) > 0 && len(*change.Children) <= standbyChildBatchLimit && change.MotherAccountId == nil && change.WorkspaceId == nil {
			children := append([]ownerapi.OperationDraftChild{}, (*change.Children)...)
			sort.Slice(children, func(i, j int) bool { return children[i].AccountId.String() < children[j].AccountId.String() })
			unique := true
			for i, c := range children {
				if c.AccountId == uuid.Nil || c.MembershipVersion < 1 || i > 0 && children[i-1].AccountId == c.AccountId {
					unique = false
					break
				}
			}
			if unique {
				members := make([]ownerapi.StandbyChildSelectionMember, 0, len(children))
				for _, c := range children {
					members = append(members, ownerapi.StandbyChildSelectionMember{AccountId: c.AccountId, MembershipVersion: c.MembershipVersion})
				}
				var current map[uuid.UUID]*uuid.UUID
				current, err = lockStandbySelection(r, tx, ownerapi.StandbyChildSelection{Members: members})
				if err == nil {
					ids := make([]uuid.UUID, 0, len(children))
					for _, child := range children {
						ids = append(ids, child.AccountId)
					}
					var complete int
					err = tx.QueryRow(r.Context(), `SELECT count(*) FROM tsw_target_accounts a JOIN tsw_target_credentials c ON c.target_account_id=a.id WHERE a.id=ANY($1) AND a.status='active' AND c.material_status='complete' AND c.materials_sealed`, ids).Scan(&complete)
					if err == nil && complete != len(children) {
						err = errDraftMaterialIncomplete
					}
					if err != nil {
						h.draftChangeError(w, r, err)
						return
					}
					var version int64
					err = tx.QueryRow(r.Context(), `SELECT version FROM tsw_standby_child_batches WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, change.BatchId).Scan(&version)
					if err == nil && version != *change.BatchVersion {
						err = errDraftConflict
					}
					for _, batch := range current {
						if batch == nil || *batch != *change.BatchId {
							err = errDraftConflict
							break
						}
					}
					if err == nil {
						_, err = tx.Exec(r.Context(), `UPDATE tsw_operation_selection_drafts SET batch_id=$2,batch_version=$3,children=$4,step='ready' WHERE owner_id=$1`, id, change.BatchId, version, mustDraftJSON(children))
						valid = true
					}
				}
			}
		}

	}
	if err != nil {
		h.draftChangeError(w, r, err)
		return
	}
	if !valid {
		writeProblem(w, r, 422, "invalid_draft", "Invalid Draft", "Select a valid item at the current step", 0)
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE tsw_operation_selection_drafts SET version=version+1,updated_at=now() WHERE owner_id=$1`, id)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		h.draftChangeError(w, r, err)
		return
	}
	h.draftResponse(w, r, id)
}
func (h *OwnerAuthHandler) draftChangeError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errDraftMaterialIncomplete) {
		writeProblem(w, r, 409, "child_material_incomplete", "Material Incomplete", "Complete the selected child material before confirming", 0)
		return
	}
	if errors.Is(err, errDraftConflict) || errors.Is(err, errStandbyConflict) || errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 409, "draft_selection_changed", "Selection Changed", "Refresh and explicitly reselect the current material", 0)
		return
	}
	h.workspaceFailure(w, r, err)
}
