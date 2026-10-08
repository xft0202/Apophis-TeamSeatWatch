package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/audit"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

func (h *OwnerAuthHandler) getBatchJoinPreview(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticated(w, r, false); !ok {
		return
	}
	preview, err := h.joinPreview(r, r.PathValue("batchId"))
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, http.StatusNotFound, "join_target_not_found", "Not Found", "The batch target was not found", 0)
		return
	}
	if err != nil {
		h.joinFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (h *OwnerAuthHandler) joinPreview(r *http.Request, batchID string) (ownerapi.JoinPreview, error) {
	tx, err := h.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return ownerapi.JoinPreview{}, err
	}
	defer tx.Rollback(r.Context())
	batch, err := batchByIDTx(r, tx, batchID)
	if err != nil {
		return ownerapi.JoinPreview{}, err
	}
	preview, _, err := h.joinPreviewTx(r, tx, batch)
	if err != nil {
		return preview, err
	}
	return preview, tx.Commit(r.Context())
}

func (h *OwnerAuthHandler) joinPreviewTx(r *http.Request, tx pgx.Tx, batch ownerapi.Batch) (ownerapi.JoinPreview, int64, error) {
	preview := ownerapi.JoinPreview{Batch: batch, TargetSeatType: "prolite", Blockers: []ownerapi.PreviewBlocker{}, OperationalState: "unknown", SnapshotCompleteness: "unknown"}
	activeBatch, conflictingAccounts, err := joinAccountConflict(r.Context(), tx, batch)
	if err != nil {
		return preview, 0, err
	}
	if activeBatch != "" {
		id := uuid.MustParse(activeBatch)
		preview.ActiveBatchId = &id
		addJoinBlocker(&preview, "join_conflict", joinAccountConflictMessage(conflictingAccounts))
	}
	if batch.MotherAccountId == nil {
		addJoinBlocker(&preview, "workspace_selection_missing", "本轮缺少母号，请重新选择操作对象")
		return preview, 0, nil
	}
	facts, id, _, err := h.selectedWorkspaceSnapshotTx(r.Context(), tx, batch.WorkspaceId, *batch.MotherAccountId)
	if errors.Is(err, pgx.ErrNoRows) {
		addJoinBlocker(&preview, "workspace_selection_expired", "母号与空间关联已变化，请重新发现并选择空间")
		return preview, 0, nil
	}
	if err != nil {
		return preview, 0, err
	}
	preview.PaidDefaultSeats, preview.MemberCount, preview.PendingInviteCount = facts.SeatLimit, facts.MemberCount, facts.PendingInviteCount
	preview.MemberSeatTypeCounts, preview.PendingInviteSeatTypeCounts = facts.MemberSeatTypeCounts, facts.PendingInviteSeatTypeCounts
	preview.EvidenceObservedAt, preview.EvidenceSource = facts.ObservedAt, facts.Source
	preview.SnapshotObservedAt, preview.SnapshotSource = facts.ObservedAt, facts.Source
	preview.SnapshotCompleteness = string(facts.Completeness)
	if batch.Status == ownerapi.BatchStatusEnded {
		addJoinBlocker(&preview, "batch_ended", "本轮已清退，请新建操作")
	}
	if facts.Status != "verified" || facts.Completeness != "complete" {
		addJoinBlocker(&preview, "workspace_facts_unavailable", "所选母号的空间同步结果未就绪或已过期，请同步空间")
	}
	if !facts.CanManage {
		addJoinBlocker(&preview, "workspace_not_manageable", "所选母号在该空间没有已确认的管理权限")
	}
	if facts.Status == "verified" && facts.CanManage {
		preview.OperationalState = "operational"
	}
	var targets, unready, wrongSeat int
	var targetIDs []uuid.UUID
	err = tx.QueryRow(r.Context(), `SELECT COALESCE(array_agg(target.id ORDER BY selected.ordinal),'{}'::uuid[]),count(*),count(*) FILTER(WHERE target.status<>'active' OR credential.material_status IS DISTINCT FROM 'complete'),
  count(*) FILTER(WHERE EXISTS(SELECT 1 FROM tsw_workspace_verification_entries entry WHERE entry.verification_id=$2 AND entry.identifier=target.identifier AND entry.seat_type IS DISTINCT FROM 'prolite'))
  FROM tsw_batch_targets selected JOIN tsw_target_accounts target ON target.id=selected.target_account_id LEFT JOIN tsw_target_credentials credential ON credential.target_account_id=target.id WHERE selected.batch_id=$1`, batch.Id, id).Scan(&targetIDs, &targets, &unready, &wrongSeat)
	if err != nil {
		return preview, id, err
	}
	if targets == 0 {
		addJoinBlocker(&preview, "target_probe_unavailable", "本轮没有目标账号")
	} else if unready > 0 {
		addJoinBlocker(&preview, "target_probe_unavailable", "部分账号不可用或认证资料不完整，请修复后继续")
	}
	if wrongSeat > 0 {
		addJoinBlocker(&preview, "target_seat_mismatch", "所选账号中存在普通、其他或未确认席位，请在空间管理核对；本轮仅使用高级席位")
	}
	capacity, newTargets, err := invitationCapacityTx(r.Context(), tx, batch.WorkspaceId, id, facts, targetIDs)
	if err != nil {
		return preview, id, err
	}
	newCount := len(newTargets)
	preview.NewInvitationCount = &newCount
	planned := min(newCount, capacity.Remaining)
	waiting := newCount - planned
	if capacity.Known {
		preview.PlannedInvitationCount, preview.WaitingSeatCount = &planned, &waiting
		preview.PaidPremiumSeats, preview.AvailablePremiumSeats = &capacity.Opened, &capacity.Remaining
		preview.OccupiedPremiumSeats, preview.ReservedPremiumSeats = &capacity.Occupied, &capacity.Reserved
	}
	if blocker := invitationCapacityBlocker(capacity, newCount); blocker != nil {
		addJoinBlocker(&preview, blocker.Code, blocker.Message)
	}
	preview.CanProceed = len(preview.Blockers) == 0
	return preview, id, nil
}

func addJoinBlocker(preview *ownerapi.JoinPreview, code, message string) {
	preview.Blockers = append(preview.Blockers, ownerapi.PreviewBlocker{Code: code, Message: message})
}

func (h *OwnerAuthHandler) createJoinOperation(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	var request ownerapi.CreateJoinOperationJSONRequestBody
	if !decodeJSON(w, r, &request) || !bool(request.Confirm) || !validConcurrency(request.Concurrency) {
		h.rejectOwnerMutation(w, r, owner, "join.create", "join_confirmation_required", http.StatusUnprocessableEntity, "join_confirmation_required", "Confirmation Required", "Explicit Owner confirmation is required")
		return
	}
	batchID := r.PathValue("batchId")
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.joinFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())

	var workspaceID, batchStatus string
	err = tx.QueryRow(r.Context(), `SELECT binding.workspace_id,batch.status FROM tsw_batches batch JOIN tsw_mother_workspace_bindings binding ON binding.id=batch.binding_id AND binding.ended_at IS NULL WHERE batch.id=$1 FOR UPDATE OF batch,binding`, batchID).Scan(&workspaceID, &batchStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		h.rejectOwnerMutation(w, r, owner, "join.create", "join_batch_not_found", 422, "join_batch_not_found", "Invalid Join Batch", "The batch was not found")
		return
	}
	if err != nil {
		h.joinFailure(w, r, err)
		return
	}

	if err = lockInvitationCapacity(r.Context(), tx, uuid.MustParse(workspaceID)); err != nil {
		h.joinFailure(w, r, err)
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT selected.target_account_id::text,target.status,(credentials.material_status='complete')
		FROM tsw_batch_targets selected
		JOIN tsw_target_accounts target ON target.id=selected.target_account_id
		LEFT JOIN tsw_target_credentials credentials ON credentials.target_account_id=target.id
		WHERE selected.batch_id=$1 ORDER BY selected.ordinal FOR UPDATE OF selected,target`, batchID)
	if err != nil {
		h.joinFailure(w, r, err)
		return
	}
	var targetIDs []string
	var invalidTargetCount int
	for rows.Next() {
		var targetID, targetStatus string
		var hasCredentials bool
		if err = rows.Scan(&targetID, &targetStatus, &hasCredentials); err != nil {
			rows.Close()
			h.joinFailure(w, r, err)
			return
		}
		targetIDs = append(targetIDs, targetID)
		if targetStatus != "active" || !hasCredentials {
			invalidTargetCount++
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		h.joinFailure(w, r, err)
		return
	}
	hashTargetIDs := append([]string(nil), targetIDs...)
	sort.Strings(hashTargetIDs)
	requestHash := sha256.Sum256([]byte("teamseatwatch:premium_join\x00" + batchID + "\x00" + strings.Join(hashTargetIDs, "\x00")))

	var existingID string
	var existingHash []byte
	var existingConcurrency int
	err = tx.QueryRow(r.Context(), `SELECT id,request_hash,task_concurrency FROM tsw_operations WHERE owner_id=$1 AND operation_type='join' AND idempotency_key=$2 FOR UPDATE`, owner.OwnerID, request.IdempotencyKey).Scan(&existingID, &existingHash, &existingConcurrency)
	if err == nil {
		if existingConcurrency != selectedConcurrency(request.Concurrency) || !bytes.Equal(existingHash, requestHash[:]) {
			h.rejectOwnerMutation(w, r, owner, "join.create", "idempotency_conflict", http.StatusConflict, "idempotency_conflict", "Conflict", "The idempotency key belongs to a different frozen target")
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			h.joinFailure(w, r, err)
			return
		}
		item, getErr := h.joinOperationByID(r, existingID)
		if getErr != nil {
			h.joinFailure(w, r, getErr)
			return
		}
		writeJSON(w, http.StatusAccepted, item)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		h.joinFailure(w, r, err)
		return
	}

	batch, err := batchByIDTx(r, tx, batchID)
	if err != nil {
		h.joinFailure(w, r, err)
		return
	}
	preview, _, err := h.joinPreviewTx(r, tx, batch)
	if err != nil {
		h.joinFailure(w, r, err)
		return
	}
	if len(targetIDs) == 0 || invalidTargetCount > 0 || batchStatus != "planned" || !preview.CanProceed {
		_ = tx.Rollback(r.Context())
		if h.writeExistingJoin(w, r, owner, request.IdempotencyKey, requestHash[:], selectedConcurrency(request.Concurrency)) {
			return
		}
		if preview.ActiveBatchId != nil {
			for _, blocker := range preview.Blockers {
				if blocker.Code == "join_conflict" {
					h.rejectOwnerMutation(w, r, owner, "join.create", "conflict", 409, "join_conflict", "Conflict", blocker.Message)
					return
				}
			}
			return
		}
		for _, blocker := range preview.Blockers {
			if blocker.Code == "premium_capacity_exceeded" || blocker.Code == "premium_capacity_unknown" {
				writeProblem(w, r, 409, blocker.Code, "Invitation capacity", blocker.Message, 0)
				return
			}
		}
		h.rejectOwnerMutation(w, r, owner, "join.create", "join_not_ready", 409, "join_not_ready", "Join Not Ready", "Current selected mother, workspace or premium-seat evidence is not sufficient")
		return
	}
	facts, currentVerificationID, _, err := h.selectedWorkspaceSnapshotTx(r.Context(), tx, batch.WorkspaceId, *batch.MotherAccountId)
	if err != nil {
		h.joinFailure(w, r, err)
		return
	}
	if facts.Status != "verified" || !facts.CanManage {
		writeProblem(w, r, 409, "workspace_not_manageable", "Invitation unavailable", "请同步空间后继续邀请", 0)
		return
	}
	verificationID := currentVerificationID

	ids := make([]uuid.UUID, len(targetIDs))
	for i, id := range targetIDs {
		ids[i] = uuid.MustParse(id)
	}
	capacity, newTargets, err := invitationCapacityTx(r.Context(), tx, batch.WorkspaceId, verificationID, facts, ids)
	if err != nil {
		h.joinFailure(w, r, err)
		return
	}
	if blocker := invitationCapacityBlocker(capacity, len(newTargets)); blocker != nil {
		writeProblem(w, r, 409, blocker.Code, "Invitation capacity", blocker.Message, 0)
		return
	}

	newTarget := map[string]bool{}
	for _, id := range newTargets {
		newTarget[id.String()] = true
	}
	remaining := capacity.Remaining
	targetIDsJSON, err := json.Marshal(targetIDs)
	if err != nil {
		h.joinFailure(w, r, err)
		return
	}
	var operationID string
	err = tx.QueryRow(r.Context(), `INSERT INTO tsw_operations(owner_id,workspace_id,batch_id,operation_type,idempotency_key,request_hash,input_snapshot,correlation_id,task_concurrency)
		VALUES ($1,$2,$3,'join',$4,$5,jsonb_build_object('workspace_id',$7::text,'batch_id',$8::text,'target_account_ids',$9::jsonb,'seat_type','prolite','verification_id',$10::bigint),$6,$11)
		RETURNING id`, owner.OwnerID, workspaceID, batchID, request.IdempotencyKey, requestHash[:], correlation(r), workspaceID, batchID, string(targetIDsJSON), verificationID, selectedConcurrency(request.Concurrency)).Scan(&operationID)
	if err == nil {
		for ordinal, targetID := range targetIDs {
			var operationTargetID string
			err = tx.QueryRow(r.Context(), `INSERT INTO tsw_operation_targets(operation_id,target_account_id,ordinal) VALUES ($1,$2,$3) RETURNING id`, operationID, targetID, ordinal+1).Scan(&operationTargetID)
			if err != nil {
				break
			}
			if newTarget[targetID] && remaining == 0 {
				_, err = tx.Exec(r.Context(), `UPDATE tsw_operation_targets SET status='blocked',outcome_code='invitation_waiting_for_seat',diagnostic_code='premium_capacity_exceeded',completed_at=now() WHERE id=$1`, operationTargetID)
				if err != nil {
					break
				}
				continue
			}
			if newTarget[targetID] {
				remaining--
			}
			_, err = tx.Exec(r.Context(), `INSERT INTO tsw_tasks(operation_target_id,workspace_id,target_account_id,task_type,dedupe_key,input_snapshot,correlation_id,max_attempts)
				VALUES ($1,$2,$3,'join',$4,jsonb_build_object('operation_target_id',$6::text,'workspace_id',$7::text,'target_account_id',$8::text),$5,3)`, operationTargetID, workspaceID, targetID, "join:"+operationTargetID, correlation(r), operationTargetID, workspaceID, targetID)
			if err != nil {
				break
			}
		}
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE tsw_batches SET status='joining',blocking_reason=NULL,updated_at=now(),version=version+1 WHERE id=$1`, batchID)
	}
	if err == nil {
		_, err = audit.Write(r.Context(), tx, audit.Event{Type: audit.JoinOperationAuthorized, Actor: audit.ActorOwner, OwnerID: owner.OwnerID, RetentionScopeID: workspaceID, EntityType: "operation", EntityID: operationID, Outcome: audit.OutcomeSucceeded, CorrelationID: correlation(r), Details: audit.OperationDetails{Operation: "join", Result: "authorized"}, IdempotencyKey: operationID + ":authorized"})
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			_ = tx.Rollback(r.Context())
			// A concurrent winner may report either the idempotency or batch-
			// operation unique constraint. Read the committed winner and
			// compare its request hash before deciding retry versus conflict.
			if h.writeExistingJoin(w, r, owner, request.IdempotencyKey, requestHash[:], selectedConcurrency(request.Concurrency)) {
				return
			}
			h.rejectOwnerMutation(w, r, owner, "join.create", "conflict", http.StatusConflict, "join_conflict", "Conflict", "本轮操作已创建，请打开原操作继续")
			return
		}
		h.joinFailure(w, r, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		h.joinFailure(w, r, err)
		return
	}
	item, err := h.joinOperationByID(r, operationID)
	if err != nil {
		h.joinFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, item)
}

func (h *OwnerAuthHandler) writeExistingJoin(w http.ResponseWriter, r *http.Request, owner ownerContext, idempotencyKey string, requestHash []byte, concurrency int) bool {
	var operationID string
	var storedHash []byte
	var storedLimit int
	err := h.pool.QueryRow(r.Context(), `SELECT id,request_hash,task_concurrency FROM tsw_operations WHERE owner_id=$1 AND operation_type='join' AND idempotency_key=$2`, owner.OwnerID, idempotencyKey).Scan(&operationID, &storedHash, &storedLimit)
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	if err != nil {
		h.joinFailure(w, r, err)
		return true
	}
	if storedLimit != concurrency || !bytes.Equal(storedHash, requestHash) {
		h.rejectOwnerMutation(w, r, owner, "join.create", "idempotency_conflict", http.StatusConflict, "idempotency_conflict", "Conflict", "The idempotency key belongs to a different frozen target")
		return true
	}
	item, err := h.joinOperationByID(r, operationID)
	if err != nil {
		h.joinFailure(w, r, err)
		return true
	}
	writeJSON(w, http.StatusAccepted, item)
	return true
}

func (h *OwnerAuthHandler) createJoinReconciliation(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	var request ownerapi.CreateJoinReconciliationJSONRequestBody
	if !decodeJSON(w, r, &request) || !validLength(request.IdempotencyKey, 8, 64) {
		h.rejectOwnerMutation(w, r, owner, "join.reconcile", "invalid_request", http.StatusUnprocessableEntity, "invalid_idempotency_key", "Invalid Request", "A stable idempotency key is required")
		return
	}
	var err error
	for count := 0; count < standbyChildBatchLimit; count++ {
		_, _, err = h.workspaceTasks.EnqueueJoinReconciliation(r.Context(), r.PathValue("batchId"), request.IdempotencyKey, correlation(r))
		if errors.Is(err, pgx.ErrNoRows) {
			err = nil
			break
		}
		if err != nil {
			h.joinFailure(w, r, err)
			return
		}
	}
	item, err := h.joinOperationByBatch(r, r.PathValue("batchId"), 1, 20)
	if err != nil {
		h.joinFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, item)
}

func (h *OwnerAuthHandler) getJoinOperation(w http.ResponseWriter, r *http.Request, params ownerapi.GetJoinOperationParams) {
	if _, ok := h.authenticated(w, r, false); !ok {
		return
	}
	page, size, ok := pagination(params.TargetPage, params.TargetPageSize)
	if !ok {
		writeProblem(w, r, http.StatusBadRequest, "invalid_pagination", "Invalid Request", "Pagination is invalid", 0)
		return
	}

	item, err := h.joinOperationByBatch(r, r.PathValue("batchId"), page, size)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, http.StatusNotFound, "join_operation_not_found", "Not Found", "The join operation was not found", 0)
		return
	}
	if err != nil {
		h.joinFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *OwnerAuthHandler) listJoinOperationsNeedingAttention(w http.ResponseWriter, r *http.Request, params ownerapi.ListJoinOperationsNeedingAttentionParams) {
	if _, ok := h.authenticated(w, r, false); !ok {
		return
	}
	page, size, ok := pagination(params.Page, params.PageSize)
	if !ok {
		writeProblem(w, r, http.StatusBadRequest, "invalid_pagination", "Invalid Request", "Pagination is invalid", 0)
		return
	}
	rows, err := h.pool.Query(r.Context(), joinOperationSelect+`,count(*) OVER() FROM tsw_operations operation WHERE operation.operation_type=$1 AND (operation.status='blocked' OR EXISTS (SELECT 1 FROM tsw_operation_targets attention_target WHERE attention_target.operation_id=operation.id AND attention_target.status IN ('failed','blocked','unknown'))) ORDER BY operation.updated_at DESC LIMIT $4 OFFSET $5`, "join", 100, 0, size, (page-1)*size)
	if err != nil {
		h.joinFailure(w, r, err)
		return
	}
	defer rows.Close()
	response := ownerapi.JoinOperationList{Items: []ownerapi.JoinOperation{}, Page: page, PageSize: size}
	for rows.Next() {
		var total int64
		item, scanErr := scanJoinOperation(rowWithTotal{Rows: rows, total: &total}, 1, 100)
		if scanErr != nil {
			h.joinFailure(w, r, scanErr)
			return
		}
		response.Total = total
		response.Items = append(response.Items, item)
	}
	if err := rows.Err(); err != nil {
		h.joinFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

const joinOperationSelect = `SELECT operation.id,operation.batch_id,operation.workspace_id,operation.status,operation.authorized_at,operation.completed_at,operation.correlation_id,operation.input_snapshot->>'seat_type',
	COALESCE((SELECT jsonb_agg(jsonb_build_object('id',page.id,'targetAccountId',COALESCE(page.target_account_id,page.membership_target_account_id),'identifier',page.identifier,'delivery',page.delivery,'status',page.status,'preflightStatus',page.preflight_status,'preflightAt',page.preflight_at,'outcomeCode',page.outcome_code,'diagnosticCode',page.diagnostic_code,'lastAttemptAt',page.last_attempt_at,'completedAt',page.completed_at,'actualSeatType',page.actual_seat_type,'invitationConfirmedAt',page.invitation_confirmed_at,'joinStage',page.platform_request_stage,'stageResults',page.stage_results,'failureDiagnosticCode',page.failure_details->>'diagnostic_code','failureStage',page.failure_details->>'stage') ORDER BY page.ordinal) FROM (SELECT target_result.*,membership.target_account_id AS membership_target_account_id,membership.seat_type AS actual_seat_type,account.identifier,
  (SELECT jsonb_build_object('membershipId',membership.id,'targetAccountId',account.id,'identifier',account.identifier,'status',a.status,'generation',a.current_generation,'cardStatus',c.status,'cardActivated',c.id IS NOT NULL,'cardDisplaySuffix',c.display_suffix,'redemptionDeadline',c.redemption_deadline,'unavailableReason',a.unavailable_reason,'loginTaskStatus',(SELECT q.status FROM tsw_tasks q WHERE q.oauth_asset_id=a.id AND q.task_type='oauth_generate' ORDER BY q.created_at DESC,q.id DESC LIMIT 1)) FROM tsw_oauth_assets a LEFT JOIN tsw_cards c ON c.membership_id=a.membership_id WHERE a.membership_id=membership.id) AS delivery,
		COALESCE((SELECT jsonb_agg(jsonb_build_object('stage',receipt.details->>'stage','httpStatus',(receipt.details->>'http_status')::int,'success',(receipt.details->>'success')::boolean,'requestSent',(receipt.details->>'request_sent')::boolean,'requestMayHaveEffect',(receipt.details->>'request_may_have_effect')::boolean,'diagnosticCode',receipt.details->>'diagnostic_code') ORDER BY receipt.occurred_at DESC,receipt.id DESC) FROM tsw_audit_events receipt WHERE receipt.entity_type='operation_target' AND receipt.entity_id=target_result.id AND receipt.event_type='join.stage_finished'),'[]'::jsonb) AS stage_results,
		(SELECT failure.details FROM tsw_audit_events failure WHERE failure.entity_type='operation_target' AND failure.entity_id=target_result.id AND failure.event_type IN ('join.target_failed','join.target_unknown') ORDER BY failure.occurred_at DESC LIMIT 1) AS failure_details
		FROM tsw_operation_targets target_result LEFT JOIN tsw_batch_memberships membership ON membership.id=target_result.membership_id JOIN tsw_target_accounts account ON account.id=COALESCE(target_result.target_account_id,membership.target_account_id) WHERE target_result.operation_id=operation.id ORDER BY target_result.ordinal LIMIT $2 OFFSET $3) page),'[]'::jsonb),
	(SELECT count(*) FROM tsw_operation_targets target_result WHERE target_result.operation_id=operation.id),
	(SELECT count(*) FROM tsw_operation_targets target_result WHERE target_result.operation_id=operation.id AND target_result.status='succeeded'),
	(SELECT count(*) FROM tsw_operation_targets target_result WHERE target_result.operation_id=operation.id AND target_result.status='failed'),
	(SELECT count(*) FROM tsw_operation_targets target_result WHERE target_result.operation_id=operation.id AND target_result.status IN ('blocked','unknown') AND COALESCE(target_result.diagnostic_code,'')<>'premium_capacity_exceeded'),
	(SELECT count(*) FROM tsw_operation_targets target_result WHERE target_result.operation_id=operation.id AND target_result.status NOT IN ('invited','succeeded','failed','blocked','unknown')),
 (SELECT count(*) FROM tsw_operation_targets t WHERE t.operation_id=operation.id AND t.invitation_confirmed_at IS NOT NULL),
 (SELECT count(*) FROM tsw_tasks q LEFT JOIN tsw_batch_memberships m ON m.id=q.membership_id WHERE q.status IN ('queued','retry_wait','running') AND (q.operation_target_id IN (SELECT id FROM tsw_operation_targets WHERE operation_id=operation.id) OR m.batch_id=operation.batch_id AND q.task_type='oauth_generate')),
 (SELECT count(*) FROM tsw_operation_targets t WHERE t.operation_id=operation.id AND ` + retryableInvitationSQL + `),
 (SELECT count(*) FROM tsw_operation_targets t WHERE t.operation_id=operation.id AND t.invitation_confirmed_at IS NULL AND t.diagnostic_code='premium_capacity_exceeded')`

func scanJoinOperation(scanner interface{ Scan(...any) error }, page, size int) (ownerapi.JoinOperation, error) {
	var item ownerapi.JoinOperation
	var targets []byte
	err := scanner.Scan(&item.Id, &item.BatchId, &item.WorkspaceId, &item.Status, &item.AuthorizedAt, &item.CompletedAt, &item.CorrelationId, &item.TargetSeatType, &targets, &item.TargetTotal, &item.SucceededCount, &item.FailedCount, &item.BlockedCount, &item.PendingCount, &item.InvitationConfirmedCount, &item.ActiveTaskCount, &item.RetryableInvitationCount, &item.WaitingSeatCount)
	if err != nil {
		return item, err
	}
	item.TargetPage, item.TargetPageSize = page, size
	return item, json.Unmarshal(targets, &item.Targets)
}

func (h *OwnerAuthHandler) joinOperationByID(r *http.Request, operationID string) (ownerapi.JoinOperation, error) {
	return scanJoinOperation(h.pool.QueryRow(r.Context(), joinOperationSelect+` FROM tsw_operations operation WHERE operation.id=$1`, operationID, 100, 0), 1, 100)
}

func (h *OwnerAuthHandler) joinOperationByBatch(r *http.Request, batchID string, page, size int) (ownerapi.JoinOperation, error) {
	return scanJoinOperation(h.pool.QueryRow(r.Context(), joinOperationSelect+` FROM tsw_operations operation WHERE operation.batch_id=$1 AND operation.operation_type='join' ORDER BY operation.created_at DESC LIMIT 1`, batchID, size, (page-1)*size), page, size)
}

// joinFailure 把底层错误记入服务端日志（带 request_id，可与 http_request 日志行对照），
// 对外只写固定的 5xx problem——不把数据库细节暴露给任何调用方。
func (h *OwnerAuthHandler) joinFailure(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("join_data_unavailable", "request_id", RequestIDFromContext(r.Context()), "error", err)
	writeProblem(w, r, http.StatusInternalServerError, "join_unavailable", "Internal Server Error", "Join data is temporarily unavailable", 0)
}
