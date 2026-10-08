package runtime

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/audit"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

// Reset detaches the selection only. Executed batches and their frozen targets survive.
func (h *OwnerAuthHandler) ResetOperationDraft(w http.ResponseWriter, r *http.Request, params ownerapi.ResetOperationDraftParams) {
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
	if params.PreviousBatchId != nil {
		previous, e := batchByIDTx(r, tx, params.PreviousBatchId.String())
		if errors.Is(e, pgx.ErrNoRows) || (e == nil && previous.Rotation.State != ownerapi.BatchRotationStateCompleted) {
			writeProblem(w, r, 409, "rotation_not_completed", "Conflict", "All original members must be confirmed removed first", 0)
			return
		}
		if e != nil {
			h.workspaceFailure(w, r, e)
			return
		}
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO tsw_operation_selection_drafts(owner_id,rotation_from_batch_id) VALUES($1,$2)
 ON CONFLICT(owner_id) DO UPDATE SET version=tsw_operation_selection_drafts.version+1,step='mother',mother_account_id=NULL,mother_revision=NULL,workspace_id=NULL,visibility_run_id=NULL,session_generation=NULL,verification_id=NULL,batch_id=NULL,batch_version=NULL,children='[]',execution_batch_id=NULL,planned_at=NULL,rotation_from_batch_id=$2,updated_at=now()`, owner.OwnerID, params.PreviousBatchId)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}

	h.draftResponse(w, r, uuid.MustParse(owner.OwnerID))
}

func (h *OwnerAuthHandler) PrepareOperationDraft(w http.ResponseWriter, r *http.Request, _ ownerapi.PrepareOperationDraftParams) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	var request ownerapi.PrepareOperationDraft
	if !decodeJSON(w, r, &request) || request.ExpectedVersion < 1 || request.PlannedAt.IsZero() {
		writeProblem(w, r, 422, "invalid_operation_plan", "Invalid Request", "A valid plan is required", 0)
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	d, err := readOperationDraft(tx.QueryRow(r.Context(), `SELECT `+draftColumns+` FROM tsw_operation_selection_drafts WHERE owner_id=$1 FOR UPDATE`, owner.OwnerID))
	if err != nil {
		h.draftChangeError(w, r, err)
		return
	}
	var batchID string
	if d.ExecutionBatchId != nil {
		batchID = d.ExecutionBatchId.String()
		var status string
		var matches bool
		err = tx.QueryRow(r.Context(), `SELECT status,binding_id=$2 AND planned_at=$3 AND source_standby_batch_id=$4 AND (SELECT count(*) FROM tsw_batch_targets WHERE batch_id=$1)=jsonb_array_length($5::jsonb) AND NOT EXISTS(SELECT 1 FROM tsw_batch_targets t WHERE t.batch_id=$1 AND NOT EXISTS(SELECT 1 FROM jsonb_to_recordset($5::jsonb) AS c("accountId" uuid) WHERE c."accountId"=t.target_account_id)) FROM tsw_batches WHERE id=$1 FOR UPDATE`, batchID, request.BindingId, request.PlannedAt, d.BatchId, mustDraftJSON(d.Children)).Scan(&status, &matches)
		if err != nil {
			h.workspaceFailure(w, r, err)
			return
		}
		if status != "planned" && status != "draft" {
			writeProblem(w, r, 409, "operation_frozen", "Conflict", "The running operation is frozen", 0)
			return
		}
		// A lost response can recover the same saved plan even after its version advanced.
		if matches {
			item, e := batchByIDTx(r, tx, batchID)
			if e == nil {
				e = tx.Commit(r.Context())
			}
			if e != nil {
				h.workspaceFailure(w, r, e)
				return
			}
			writeJSON(w, 200, item)
			return
		}
	}
	if d.Version != request.ExpectedVersion || d.BatchId == nil || d.WorkspaceId == nil || d.MotherAccountId == nil || len(d.Children) == 0 {
		h.draftChangeError(w, r, errDraftConflict)
		return
	}
	if d.PreviousBatchId != nil {
		previous, e := batchByIDTx(r, tx, d.PreviousBatchId.String())
		if e != nil {
			h.workspaceFailure(w, r, e)
			return
		}
		if previous.Rotation.State != ownerapi.BatchRotationStateCompleted || previous.BindingId != request.BindingId || (previous.SourceBatchId != nil && *previous.SourceBatchId == *d.BatchId) {
			writeProblem(w, r, 409, "invalid_rotation_successor", "Conflict", "The previous rotation or selected source batch is invalid", 0)
			return
		}
	}
	if !request.PlannedAt.After(time.Now()) {
		writeProblem(w, r, 422, "invalid_planned_at", "Invalid Request", "Planned removal must be in the future", 0)
		return
	}
	if err = checkOperationDraft(r, tx, &d); err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	if !d.MotherCurrent || !d.WorkspaceCurrent || !d.BatchCurrent {
		h.draftChangeError(w, r, errDraftConflict)
		return
	}
	_, manageable, err := selectedWorkspaceMotherAuthority(r.Context(), tx, *d.VerificationId, *d.MotherAccountId)
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	if !manageable {
		writeProblem(w, r, 409, "workspace_not_manageable", "Conflict", "This mother cannot manage the selected workspace", 0)
		return
	}
	var workspaceID string
	err = tx.QueryRow(r.Context(), `SELECT workspace_id FROM tsw_mother_workspace_bindings WHERE id=$1 AND mother_account_id=$2 AND workspace_id=$3 AND status='active' AND ended_at IS NULL FOR UPDATE`, request.BindingId, d.MotherAccountId, d.WorkspaceId).Scan(&workspaceID)
	if err != nil {
		h.draftChangeError(w, r, err)
		return
	}
	var sourceName string
	err = tx.QueryRow(r.Context(), `SELECT name FROM tsw_standby_child_batches WHERE id=$1 AND deleted_at IS NULL AND version=$2 FOR SHARE`, d.BatchId, d.BatchVersion).Scan(&sourceName)
	if err != nil {
		h.draftChangeError(w, r, err)
		return
	}
	selection := ownerapi.StandbyChildSelection{Members: []ownerapi.StandbyChildSelectionMember{}}
	ids := make([]uuid.UUID, 0, len(d.Children))
	for _, child := range d.Children {
		selection.Members = append(selection.Members, ownerapi.StandbyChildSelectionMember{AccountId: child.AccountId, MembershipVersion: child.MembershipVersion})
		ids = append(ids, child.AccountId)
	}
	memberships, err := lockStandbySelection(r, tx, selection)
	if err != nil {
		h.draftChangeError(w, r, err)
		return
	}
	for _, source := range memberships {
		if source == nil || *source != *d.BatchId {
			h.draftChangeError(w, r, errDraftConflict)
			return
		}
	}
	event := audit.BatchCreated
	result := "created"
	if batchID == "" {
		err = tx.QueryRow(r.Context(), `INSERT INTO tsw_batches(binding_id,sequence_no,status,planned_at,source_standby_batch_id,source_batch_name) SELECT $1,COALESCE(max(sequence_no),0)+1,'planned',$2,$3,$4 FROM tsw_batches WHERE binding_id=$1 RETURNING id`, request.BindingId, request.PlannedAt, d.BatchId, sourceName).Scan(&batchID)
	} else {
		event = audit.BatchUpdated
		result = "updated"
		_, err = tx.Exec(r.Context(), `UPDATE tsw_batches SET sequence_no=CASE WHEN binding_id=$2 THEN sequence_no ELSE (SELECT COALESCE(max(sequence_no),0)+1 FROM tsw_batches WHERE binding_id=$2) END,binding_id=$2,planned_at=$3,source_standby_batch_id=$4,source_batch_name=$5,updated_at=now(),version=version+1 WHERE id=$1`, batchID, request.BindingId, request.PlannedAt, d.BatchId, sourceName)
		if err == nil {
			_, err = tx.Exec(r.Context(), `DELETE FROM tsw_batch_targets WHERE batch_id=$1`, batchID)
		}
	}
	if err == nil {
		err = insertBatchTargets(r, tx, batchID, ids)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE tsw_operation_selection_drafts SET execution_batch_id=$2,planned_at=$3,version=version+1,updated_at=now() WHERE owner_id=$1`, owner.OwnerID, batchID, request.PlannedAt)
	}
	var item ownerapi.Batch
	if err == nil {
		item, err = batchByIDTx(r, tx, batchID)
	}
	if err == nil {
		_, err = audit.Write(r.Context(), tx, audit.Event{Type: event, Actor: audit.ActorOwner, OwnerID: owner.OwnerID, RetentionScopeID: workspaceID, EntityType: "batch", EntityID: batchID, Outcome: audit.OutcomeSucceeded, CorrelationID: correlation(r), Details: audit.BatchDetails{Result: result}, IdempotencyKey: batchID + ":operation-prepared:" + strconv.FormatInt(item.Version, 10)})
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		h.batchConflict(w, r, owner, "operation.prepare", err)
		return
	}
	writeJSON(w, 200, item)
}

func (h *OwnerAuthHandler) StartBatchAccountLogin(w http.ResponseWriter, r *http.Request, batchID uuid.UUID, _ ownerapi.StartBatchAccountLoginParams) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	var request ownerapi.StartAccountLogin
	if !decodeJSON(w, r, &request) || !validLength(request.IdempotencyKey, 8, 64) || !validConcurrency(request.Concurrency) {
		writeProblem(w, r, 422, "invalid_login_request", "Invalid Request", "A stable login request is required", 0)
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var workspaceID string
	var canStart bool
	err = tx.QueryRow(r.Context(), `SELECT binding.workspace_id,batch.status IN ('joining','serving') AND EXISTS(SELECT 1 FROM tsw_operations op JOIN tsw_operation_targets t ON t.operation_id=op.id WHERE op.batch_id=batch.id AND op.operation_type='join') AND (SELECT count(*) FROM tsw_operations op JOIN tsw_operation_targets t ON t.operation_id=op.id WHERE op.batch_id=batch.id AND op.operation_type='join')=(SELECT count(*) FROM tsw_batch_targets WHERE batch_id=batch.id) AND EXISTS(SELECT 1 FROM tsw_operations op JOIN tsw_operation_targets t ON t.operation_id=op.id WHERE op.batch_id=batch.id AND op.operation_type='join' AND t.invitation_confirmed_at IS NOT NULL) FROM tsw_batches batch JOIN tsw_mother_workspace_bindings binding ON binding.id=batch.binding_id WHERE batch.id=$1 FOR UPDATE OF batch`, batchID).Scan(&workspaceID, &canStart)
	if err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	if !canStart {
		writeProblem(w, r, 409, "invitation_incomplete", "Conflict", "尚无已确认的高级席位邀请，请先发送邀请；已确认账号可单独继续 OAuth 登录", 0)
		return
	}
	if request.TargetAccountId != nil {
		var valid bool
		err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM tsw_operations op JOIN tsw_operation_targets t ON t.operation_id=op.id LEFT JOIN tsw_batch_memberships m ON m.id=t.membership_id JOIN tsw_batch_targets frozen ON frozen.batch_id=op.batch_id AND frozen.target_account_id=COALESCE(t.target_account_id,m.target_account_id) WHERE COALESCE(t.target_account_id,m.target_account_id)=$1 AND (t.membership_id IS NULL OR m.state='active') AND op.batch_id=$2 AND op.operation_type='join' AND t.invitation_confirmed_at IS NOT NULL)`, request.TargetAccountId, batchID).Scan(&valid)
		if err != nil {
			h.deliveryFailure(w, r, err)
			return
		}
		if !valid {
			writeProblem(w, r, 422, "invalid_login_target", "Invalid Request", "Choose an invited account of this operation", 0)
			return
		}
	}
	var savedLimit int
	var loginPending bool
	err = tx.QueryRow(r.Context(), `SELECT b.login_task_concurrency,EXISTS(SELECT 1 FROM tsw_tasks t WHERE t.concurrency_key='login:'||b.id::text AND t.status IN ('queued','retry_wait','running')) FROM tsw_batches b WHERE b.id=$1`, batchID).Scan(&savedLimit, &loginPending)
	if err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	if loginPending && savedLimit != selectedConcurrency(request.Concurrency) {
		writeProblem(w, r, 409, "login_concurrency_frozen", "Conflict", "Wait for the current login task before changing concurrency", 0)
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE tsw_batches SET login_started_at=COALESCE(login_started_at,now()),login_task_concurrency=$2,updated_at=now(),version=version+1 WHERE id=$1`, batchID, selectedConcurrency(request.Concurrency))
	var queued int64
	if err == nil {
		result, e := tx.Exec(r.Context(), `INSERT INTO tsw_tasks(operation_target_id,workspace_id,target_account_id,task_type,dedupe_key,input_snapshot,correlation_id,max_attempts)
   SELECT t.id,$2::uuid,t.target_account_id,'join','workspace-oauth:'||t.id::text||':'||$3,jsonb_build_object('operation_target_id',t.id::text,'workspace_id',$2::uuid::text,'target_account_id',t.target_account_id::text),$4,3
   FROM tsw_operations op JOIN tsw_operation_targets t ON t.operation_id=op.id WHERE op.batch_id=$1 AND op.operation_type='join' AND t.invitation_confirmed_at IS NOT NULL AND t.status IN ('invited','failed','blocked','unknown') AND t.membership_id IS NULL AND ($5::uuid IS NULL OR t.target_account_id=$5)
   AND NOT EXISTS(SELECT 1 FROM tsw_tasks q WHERE q.operation_target_id=t.id AND q.status IN ('queued','retry_wait','running')) ON CONFLICT(task_type,dedupe_key) DO NOTHING`, batchID, workspaceID, request.IdempotencyKey, correlation(r), request.TargetAccountId)
		err = e
		if e == nil {
			queued = result.RowsAffected()
		}
		if err == nil && queued > 0 {
			_, err = tx.Exec(r.Context(), `UPDATE tsw_operation_targets t SET status='queued',completed_at=NULL,updated_at=now(),version=t.version+1 FROM tsw_operations op WHERE op.id=t.operation_id AND op.batch_id=$1 AND t.invitation_confirmed_at IS NOT NULL AND t.status IN ('invited','failed','blocked','unknown') AND EXISTS(SELECT 1 FROM tsw_tasks q WHERE q.operation_target_id=t.id AND q.task_type='join' AND q.dedupe_key='workspace-oauth:'||t.id::text||':'||$2 AND q.status IN ('queued','retry_wait','running'))`, batchID, request.IdempotencyKey)
			if err == nil {
				_, err = tx.Exec(r.Context(), `UPDATE tsw_operations SET status='queued',completed_at=NULL,updated_at=now(),version=version+1 WHERE batch_id=$1 AND operation_type='join'`, batchID)
			}
		}
	}
	if err == nil {
		result, e := tx.Exec(r.Context(), `INSERT INTO tsw_tasks(membership_id,oauth_asset_id,workspace_id,task_type,dedupe_key,input_snapshot,correlation_id,max_attempts)
   SELECT m.id,a.id,$2::uuid,'oauth_generate','oauth-generate:'||a.id::text||':'||$3,jsonb_build_object('membership_id',m.id::text,'oauth_asset_id',a.id::text,'workspace_id',$2::uuid::text),$4,3
   FROM tsw_batch_memberships m JOIN tsw_oauth_assets a ON a.membership_id=m.id
   WHERE m.batch_id=$1 AND m.state='active' AND ($5::uuid IS NULL OR m.target_account_id=$5) AND a.status IN ('pending','unavailable')
    AND a.current_delivery_version_id IS NULL AND NOT EXISTS(SELECT 1 FROM tsw_tasks t WHERE t.oauth_asset_id=a.id AND t.task_type='oauth_generate' AND t.status IN ('queued','retry_wait','running'))
   ON CONFLICT(task_type,dedupe_key) DO NOTHING`, batchID, workspaceID, request.IdempotencyKey, correlation(r), request.TargetAccountId)
		err = e
		if e == nil {
			queued += result.RowsAffected()
		}
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE tsw_oauth_assets a SET status='pending',unavailable_reason=NULL,updated_at=now(),version=a.version+1 FROM tsw_tasks t WHERE t.oauth_asset_id=a.id AND t.dedupe_key='oauth-generate:'||a.id::text||':'||$1 AND t.status='queued' AND a.status='unavailable'`, request.IdempotencyKey)
	}
	if err == nil {
		_, err = audit.Write(r.Context(), tx, audit.Event{Type: audit.AccountLoginAuthorized, Actor: audit.ActorOwner, OwnerID: owner.OwnerID, RetentionScopeID: workspaceID, EntityType: "batch", EntityID: batchID.String(), Outcome: audit.OutcomeSucceeded, CorrelationID: correlation(r), Details: audit.BatchDetails{Result: "updated"}, IdempotencyKey: batchID.String() + ":login:" + request.IdempotencyKey})
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	writeJSON(w, 202, ownerapi.ProbeDeliveryResponse{BatchId: batchID, Queued: int(queued)})
}

func (h *OwnerAuthHandler) RetryBatchInvitation(w http.ResponseWriter, r *http.Request, batchID uuid.UUID, _ ownerapi.RetryBatchInvitationParams) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	var request ownerapi.RefreshJoinRequest
	if !decodeJSON(w, r, &request) || !validLength(request.IdempotencyKey, 8, 64) {
		writeProblem(w, r, 422, "invalid_retry_request", "Invalid Request", "A stable request is required", 0)
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.joinFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var status string
	err = tx.QueryRow(r.Context(), `SELECT status FROM tsw_batches WHERE id=$1 FOR UPDATE`, batchID).Scan(&status)
	if err != nil {
		h.joinFailure(w, r, err)
		return
	}
	if status != "joining" && status != "serving" {
		writeProblem(w, r, 409, "retry_not_available", "Conflict", "This batch has no invitations to retry", 0)
		return
	}
	var operationID, workspaceID string
	err = tx.QueryRow(r.Context(), `SELECT id,workspace_id FROM tsw_operations WHERE batch_id=$1 AND operation_type='join' ORDER BY created_at DESC LIMIT 1 FOR UPDATE`, batchID).Scan(&operationID, &workspaceID)
	if err != nil {
		h.joinFailure(w, r, err)
		return
	}
	if err = lockInvitationCapacity(r.Context(), tx, uuid.MustParse(workspaceID)); err != nil {
		h.joinFailure(w, r, err)
		return
	}

	// Only explicitly unsent, unconfirmed targets may be replayed.
	rows, err := tx.Query(r.Context(), `SELECT t.id,t.target_account_id FROM tsw_operation_targets t WHERE t.operation_id=$1 AND `+retryableInvitationSQL+` ORDER BY t.ordinal FOR UPDATE OF t`, operationID)
	if err != nil {
		h.joinFailure(w, r, err)
		return
	}
	type retryTarget struct{ id, account string }
	targets := []retryTarget{}
	for rows.Next() {
		var item retryTarget
		if err = rows.Scan(&item.id, &item.account); err != nil {
			break
		}
		targets = append(targets, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		h.joinFailure(w, r, err)
		return
	}
	batch, err := batchByIDTx(r, tx, batchID.String())
	if err != nil {
		h.joinFailure(w, r, err)
		return
	}
	facts, verificationID, _, err := h.selectedWorkspaceSnapshotTx(r.Context(), tx, batch.WorkspaceId, *batch.MotherAccountId)
	if err != nil || facts.Status != "verified" || !facts.CanManage {
		writeProblem(w, r, 409, "workspace_not_manageable", "Conflict", "请同步空间后继续邀请", 0)
		return
	}
	ids := make([]uuid.UUID, len(targets))
	for i, target := range targets {
		ids[i] = uuid.MustParse(target.account)
	}
	capacity, newTargets, err := invitationCapacityTx(r.Context(), tx, batch.WorkspaceId, verificationID, facts, ids)
	if err != nil {
		h.joinFailure(w, r, err)
		return
	}
	if !capacity.Known && len(newTargets) > 0 {
		writeProblem(w, r, 409, "premium_capacity_unknown", "Conflict", "请同步高级席位开通数后继续邀请", 0)
		return
	}
	newTarget := map[string]bool{}
	for _, id := range newTargets {
		newTarget[id.String()] = true
	}
	remaining := capacity.Remaining
	queuedCount := 0

	for _, target := range targets {
		if newTarget[target.account] && remaining == 0 {
			_, err = tx.Exec(r.Context(), `UPDATE tsw_operation_targets SET status='blocked',outcome_code='invitation_waiting_for_seat',diagnostic_code='premium_capacity_exceeded' WHERE id=$1`, target.id)
			if err != nil {
				break
			}
			continue
		}
		if newTarget[target.account] {
			remaining--
		}

		result, e := tx.Exec(r.Context(), `INSERT INTO tsw_tasks(operation_target_id,workspace_id,target_account_id,task_type,dedupe_key,input_snapshot,correlation_id,max_attempts) VALUES($1::uuid,$2::uuid,$3::uuid,'join',$4,jsonb_build_object('operation_target_id',$1::uuid::text,'workspace_id',$2::uuid::text,'target_account_id',$3::uuid::text),$5,3) ON CONFLICT(task_type,dedupe_key) DO NOTHING`, target.id, workspaceID, target.account, "join-retry:"+target.id+":"+request.IdempotencyKey, correlation(r))
		err = e
		if err == nil && result.RowsAffected() == 1 {
			queuedCount++
			_, err = tx.Exec(r.Context(), `UPDATE tsw_operation_targets SET status='queued',outcome_code=NULL,diagnostic_code=NULL,completed_at=NULL,platform_request_may_have_reached=false,platform_request_stage=NULL,platform_request_started_at=NULL,updated_at=now(),version=version+1 WHERE id=$1`, target.id)
		}
		if err != nil {
			break
		}
	}
	if err == nil && queuedCount > 0 {
		_, err = tx.Exec(r.Context(), `UPDATE tsw_operations SET status='queued',completed_at=NULL,updated_at=now(),version=version+1 WHERE id=$1`, operationID)
	}
	if err == nil {
		_, err = audit.Write(r.Context(), tx, audit.Event{Type: audit.JoinOperationAuthorized, Actor: audit.ActorOwner, OwnerID: owner.OwnerID, RetentionScopeID: workspaceID, EntityType: "operation", EntityID: operationID, Outcome: audit.OutcomeSucceeded, CorrelationID: correlation(r), Details: audit.OperationDetails{Operation: "join", Result: "authorized"}, IdempotencyKey: operationID + ":retry:" + request.IdempotencyKey})
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		h.joinFailure(w, r, err)
		return
	}
	item, err := h.joinOperationByBatch(r, batchID.String(), 1, 20)
	if err != nil {
		h.joinFailure(w, r, err)
		return
	}
	writeJSON(w, 202, item)
}

func (h *OwnerAuthHandler) GetMotherAccount(w http.ResponseWriter, r *http.Request, accountID uuid.UUID) {
	if _, ok := h.authenticated(w, r, false); !ok {
		return
	}
	var item ownerapi.MotherAccount
	err := h.pool.QueryRow(r.Context(), `SELECT a.id,a.display_name,a.platform_account_ref,a.status,a.version,a.updated_at,c.login_identifier,CASE WHEN c.totp_secret IS NULL THEN 'needs_totp' ELSE 'complete' END FROM tsw_mother_accounts a JOIN tsw_mother_account_credentials c ON c.mother_account_id=a.id WHERE a.id=$1`, accountID).Scan(&item.Id, &item.DisplayName, &item.PlatformAccountRef, &item.Status, &item.Version, &item.UpdatedAt, &item.LoginIdentifier, &item.MaterialStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "mother_not_found", "Not Found", "Mother account was not found", 0)
		return
	}
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	item.AccessStatus = ownerapi.MotherAccountAccessStatusNotVerified
	writeJSON(w, 200, item)
}
