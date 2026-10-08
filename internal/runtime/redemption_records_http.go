package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

// An order is a historical redemption. Card inventory state never changes that fact.
const redemptionReclaimStateSQL = `(CASE WHEN latest.id IS NULL THEN 'not_requested'
 WHEN latest.status='queued' THEN 'queued' WHEN latest.status IN ('running','retry_wait') THEN 'running'
 WHEN latest.status='succeeded' AND latest.reclaim_result='probe_ok' THEN 'healthy'
 WHEN latest.status='succeeded' AND latest.reclaim_result IN ('token_refresh','full_relogin') THEN 'restored'
 WHEN latest.status='failed' AND latest.reclaim_result='unrecoverable' THEN 'unrecoverable'
 ELSE 'pending_check' END)`
const redemptionAccessSQL = `(card.status='active' AND membership.state='active' AND batch.status IN ('serving','removing')
 AND asset.status='ready' AND asset.current_delivery_version_id IS NOT NULL AND ord.current_delivery_version_id=asset.current_delivery_version_id)`
const redemptionCredentialStateSQL = `(CASE WHEN card.status='revoked' THEN 'revoked'
 WHEN membership.state<>'active' OR batch.status NOT IN ('serving','removing') THEN 'ended'
 WHEN asset.status='reclaiming' OR latest.status IN ('queued','running','retry_wait') THEN 'reclaiming'
 WHEN asset.status='unavailable' AND latest.status='failed' AND latest.reclaim_result='unrecoverable' THEN 'unavailable'
 WHEN asset.liveness_status='auth_error' THEN 'needs_reclaim'
 WHEN asset.liveness_status='deactivated_workspace' THEN 'unavailable'
 WHEN ` + redemptionAccessSQL + ` AND asset.liveness_status='ok' THEN 'healthy'
 ELSE 'pending_check' END)`
const redemptionCanAuthorizeSQL = `(card.status='active' AND membership.state='active' AND batch.status IN ('serving','removing')
 AND ord.current_delivery_version_id=asset.current_delivery_version_id AND asset.status='unavailable'
 AND latest.status='failed' AND latest.reclaim_result='unrecoverable')`
const redemptionJoins = ` FROM tsw_orders ord
 JOIN tsw_batch_memberships membership ON membership.id=ord.membership_id
 JOIN tsw_batches batch ON batch.id=membership.batch_id
 JOIN tsw_target_accounts target ON target.id=membership.target_account_id
 JOIN tsw_mother_workspace_bindings binding ON binding.id=batch.binding_id
 JOIN tsw_mother_accounts mother ON mother.id=binding.mother_account_id
 LEFT JOIN tsw_mother_account_credentials mother_credential ON mother_credential.mother_account_id=mother.id
 JOIN tsw_workspaces workspace ON workspace.id=binding.workspace_id
 JOIN tsw_cards card ON card.id=ord.card_id AND card.membership_id=membership.id
 JOIN tsw_oauth_assets asset ON asset.id=ord.oauth_asset_id AND asset.membership_id=membership.id
 LEFT JOIN LATERAL (SELECT task.id,task.status,task.reclaim_result,task.reclaim_stage,task.created_at,task.updated_at,
 task.input_snapshot->>'origin' AS origin FROM tsw_tasks task WHERE task.oauth_asset_id=asset.id AND task.task_type='oauth_reclaim'
 ORDER BY task.created_at DESC,task.id DESC LIMIT 1) latest ON TRUE`
const redemptionSelect = `SELECT membership.id,ord.id,target.id,target.identifier,mother.id,COALESCE(NULLIF(mother_credential.login_identifier,''),mother.display_name),
 workspace.id,workspace.display_name,batch.id,batch.sequence_no,batch.status,batch.source_standby_batch_id,batch.source_batch_name,
 ord.created_at,card.display_suffix,card.created_at,card.status='revoked',membership.state<>'active' OR batch.status NOT IN ('serving','removing'),
 asset.status,` + redemptionCredentialStateSQL + `,` + redemptionReclaimStateSQL + `,` + redemptionAccessSQL + `,COALESCE(` + redemptionCanAuthorizeSQL + `,false),
 asset.probed_at,latest.created_at,latest.updated_at,latest.origin,latest.reclaim_stage` + redemptionJoins
const redemptionFilter = ` WHERE mother.id=$1 AND workspace.id=$2
 AND ($3::text IS NULL OR target.identifier ILIKE '%'||$3||'%' OR card.display_suffix ILIKE '%'||$3||'%' OR batch.source_batch_name ILIKE '%'||$3||'%')
 AND ($4::uuid IS NULL OR batch.source_standby_batch_id=$4) AND ($5::uuid IS NULL OR membership.id=$5)
 AND ($6::timestamptz IS NULL OR ord.created_at >= $6) AND ($7::timestamptz IS NULL OR ord.created_at < $7)
 AND ($8::text IS NULL OR ` + redemptionCredentialStateSQL + `=$8) AND ($9::text IS NULL OR ` + redemptionReclaimStateSQL + `=$9)`

func scanRedemptionRecord(row interface{ Scan(...any) error }) (ownerapi.RedemptionRecord, error) {
	var v ownerapi.RedemptionRecord
	err := row.Scan(&v.MembershipId, &v.OrderId, &v.TargetAccountId, &v.TargetIdentifier, &v.MotherAccountId, &v.MotherIdentifier,
		&v.WorkspaceId, &v.WorkspaceName, &v.BatchId, &v.BatchSequenceNo, &v.BatchStatus, &v.SourceBatchId, &v.SourceBatchName,
		&v.RedeemedAt, &v.CardDisplaySuffix, &v.CardGeneratedAt, &v.CardRevoked, &v.ServiceEnded, &v.AssetStatus, &v.CredentialState, &v.ReclaimState,
		&v.AccessAvailable, &v.CanAuthorizeReclaim, &v.ProbedAt, &v.ReclaimRequestedAt, &v.ReclaimUpdatedAt, &v.ReclaimOrigin, &v.ReclaimStage)
	return v, err
}
func validRedemptionFilters(p ownerapi.ListRedemptionRecordsParams) bool {
	return p.MotherAccountId != uuid.Nil && p.WorkspaceId != uuid.Nil &&
		(p.CredentialState == nil || p.CredentialState.Valid()) && (p.ReclaimState == nil || p.ReclaimState.Valid()) &&
		len(strings.TrimSpace(stringValue(p.Search))) <= 254 &&
		(p.RedeemedFrom == nil || p.RedeemedBefore == nil || p.RedeemedFrom.Before(*p.RedeemedBefore))
}
func (h *OwnerAuthHandler) listRedemptionRecords(w http.ResponseWriter, r *http.Request, p ownerapi.ListRedemptionRecordsParams) {
	if _, ok := h.authenticated(w, r, false); !ok {
		return
	}
	page, size, valid := redemptionHistoryPagination(p.Page, p.PageSize)
	if !valid || !validRedemptionFilters(p) {
		writeProblem(w, r, 400, "invalid_redemption_filter", "Invalid Request", "Choose a valid scope, time range and filters", 0)
		return
	}
	tx, err := h.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	args := []any{p.MotherAccountId, p.WorkspaceId, nullableDeliverySearch(strings.TrimSpace(stringValue(p.Search))), p.SourceBatchId, p.MembershipId, p.RedeemedFrom, p.RedeemedBefore, p.CredentialState, p.ReclaimState}
	v := ownerapi.RedemptionRecordList{Page: page, PageSize: ownerapi.RedemptionRecordListPageSize(size), Items: []ownerapi.RedemptionRecord{}}
	if err = tx.QueryRow(r.Context(), `SELECT count(*)`+redemptionJoins+redemptionFilter, args...).Scan(&v.Total); err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	rows, err := tx.Query(r.Context(), redemptionSelect+redemptionFilter+` ORDER BY ord.created_at DESC,ord.id DESC LIMIT $10 OFFSET $11`, append(args, size, (page-1)*size)...)
	if err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		item, e := scanRedemptionRecord(rows)
		if e != nil {
			h.deliveryFailure(w, r, e)
			return
		}
		v.Items = append(v.Items, item)
	}
	if err = rows.Err(); err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	rows.Close()
	if err = tx.Commit(r.Context()); err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	writeNoStoreJSON(w, 200, v)
}

func (h *OwnerAuthHandler) listRedemptionSourceBatches(w http.ResponseWriter, r *http.Request, p ownerapi.ListRedemptionSourceBatchesParams) {
	if _, ok := h.authenticated(w, r, false); !ok {
		return
	}
	page, size, valid := redemptionHistoryPagination(p.Page, p.PageSize)
	if !valid || p.MotherAccountId == uuid.Nil || p.WorkspaceId == uuid.Nil || len(stringValue(p.Search)) > 254 {
		writeProblem(w, r, 400, "invalid_redemption_filter", "Invalid Request", "Choose a valid scope and search", 0)
		return
	}
	tx, err := h.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	const grouped = `SELECT batch.source_standby_batch_id AS id,(array_agg(batch.source_batch_name ORDER BY batch.created_at DESC,batch.id))[1] AS name` + redemptionJoins + ` WHERE mother.id=$1 AND workspace.id=$2 AND batch.source_standby_batch_id IS NOT NULL GROUP BY batch.source_standby_batch_id`
	args := []any{p.MotherAccountId, p.WorkspaceId, nullableDeliverySearch(strings.TrimSpace(stringValue(p.Search)))}
	v := ownerapi.RedemptionSourceBatchList{Page: page, PageSize: ownerapi.RedemptionSourceBatchListPageSize(size)}
	v.Items = make([]struct {
		Id   uuid.UUID `json:"id"`
		Name string    `json:"name"`
	}, 0, size)
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM (`+grouped+`) sources WHERE ($3::text IS NULL OR name ILIKE '%'||$3||'%')`, args...).Scan(&v.Total); err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT id,name FROM (`+grouped+`) sources WHERE ($3::text IS NULL OR name ILIKE '%'||$3||'%') ORDER BY name,id LIMIT $4 OFFSET $5`, append(args, size, (page-1)*size)...)
	if err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var item struct {
			Id   uuid.UUID `json:"id"`
			Name string    `json:"name"`
		}
		if err = rows.Scan(&item.Id, &item.Name); err != nil {
			h.deliveryFailure(w, r, err)
			return
		}
		v.Items = append(v.Items, item)
	}
	if err = rows.Err(); err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	rows.Close()
	if err = tx.Commit(r.Context()); err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	writeNoStoreJSON(w, 200, v)
}

func redemptionHistoryPagination(page *int, size *int) (int, int, bool) {
	p, s, ok := pagination(page, size)
	return p, s, ok && (s == 20 || s == 50 || s == 100)
}
func (h *OwnerAuthHandler) getRedemptionRecord(w http.ResponseWriter, r *http.Request, id ownerapi.MembershipId, p ownerapi.GetRedemptionRecordParams) {
	if _, ok := h.authenticated(w, r, false); !ok {
		return
	}
	var reclaimSize, timelineSize *int
	if p.ReclaimPageSize != nil {
		v := int(*p.ReclaimPageSize)
		reclaimSize = &v
	}
	if p.TimelinePageSize != nil {
		v := int(*p.TimelinePageSize)
		timelineSize = &v
	}
	rp, rs, rok := redemptionHistoryPagination(p.ReclaimPage, reclaimSize)
	tp, ts, tok := redemptionHistoryPagination(p.TimelinePage, timelineSize)
	if !rok || !tok || p.MotherAccountId == uuid.Nil || p.WorkspaceId == uuid.Nil {
		writeProblem(w, r, 400, "invalid_redemption_filter", "Invalid Request", "Choose a valid scope and pagination", 0)
		return
	}
	tx, err := h.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	record, err := scanRedemptionRecord(tx.QueryRow(r.Context(), redemptionSelect+` WHERE membership.id=$1 AND mother.id=$2 AND workspace.id=$3`, id, p.MotherAccountId, p.WorkspaceId))
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "redemption_not_found", "Not Found", "No redeemed order exists in this scope", 0)
		return
	}
	if err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	v := ownerapi.RedemptionRecordDetail{Record: record}
	v.OriginalDelivery, err = redemptionVersion(r.Context(), tx, record.OrderId, true)
	if err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	v.CurrentDelivery, err = redemptionVersion(r.Context(), tx, record.OrderId, false)
	if err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	v.Reclaims, err = redemptionReclaims(r.Context(), tx, record.OrderId, rp, rs)
	if err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	v.Timeline, err = redemptionTimeline(r.Context(), tx, record.OrderId, tp, ts)
	if err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	writeNoStoreJSON(w, 200, v)
}
func redemptionVersion(ctx context.Context, tx pgx.Tx, orderID uuid.UUID, original bool) (*ownerapi.RedemptionDeliveryVersion, error) {
	column := "ord.current_delivery_version_id"
	if original {
		column = "ord.original_delivery_version_id"
	}
	var v ownerapi.RedemptionDeliveryVersion
	err := tx.QueryRow(ctx, `SELECT version.generation,version.created_at,version.validated_workspace_id FROM tsw_orders ord JOIN tsw_delivery_versions version ON version.id=`+column+` AND version.oauth_asset_id=ord.oauth_asset_id WHERE ord.id=$1`, orderID).Scan(&v.Generation, &v.CreatedAt, &v.WorkspaceId)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &v, err
}
func redemptionReclaims(ctx context.Context, tx pgx.Tx, orderID uuid.UUID, page, size int) (ownerapi.RedemptionReclaimList, error) {
	v := ownerapi.RedemptionReclaimList{Items: []ownerapi.RedemptionReclaimRecord{}, Page: page, PageSize: ownerapi.RedemptionReclaimListPageSize(size)}
	const joins = ` FROM tsw_tasks latest JOIN tsw_orders ord ON ord.oauth_asset_id=latest.oauth_asset_id WHERE ord.id=$1 AND latest.task_type='oauth_reclaim'`
	if err := tx.QueryRow(ctx, `SELECT count(*)`+joins, orderID).Scan(&v.Total); err != nil {
		return v, err
	}
	rows, err := tx.Query(ctx, `SELECT latest.id,latest.created_at,latest.updated_at,latest.finished_at,COALESCE(latest.input_snapshot->>'origin','unknown'),latest.reclaim_stage,`+redemptionReclaimStateSQL+`,
 (SELECT version.generation FROM tsw_delivery_versions version WHERE version.oauth_asset_id=latest.oauth_asset_id
   AND version.id=NULLIF(latest.input_snapshot->>'delivery_version_id','')::uuid),
 (SELECT version.generation FROM tsw_oauth_attempts attempt JOIN tsw_delivery_versions version ON version.id=attempt.published_version_id
   WHERE attempt.task_id=latest.id ORDER BY attempt.started_at DESC,attempt.id DESC LIMIT 1)`+joins+` ORDER BY latest.created_at DESC,latest.id DESC LIMIT $2 OFFSET $3`, orderID, size, (page-1)*size)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var item ownerapi.RedemptionReclaimRecord
		if err = rows.Scan(&item.Id, &item.RequestedAt, &item.UpdatedAt, &item.FinishedAt, &item.Origin, &item.Stage, &item.State, &item.PreviousGeneration, &item.ReplacementGeneration); err != nil {
			return v, err
		}
		v.Items = append(v.Items, item)
	}
	return v, rows.Err()
}

// Business history excludes read-only polling events. Immutable version rows and
// order creation remain visible even when audit retention removes older events.
const redemptionTimelineSQL = `WITH facts AS (SELECT ord.*,card.created_at AS generated_at,card.id AS bound_card_id,membership.removed_at
 FROM tsw_orders ord JOIN tsw_cards card ON card.id=ord.card_id JOIN tsw_batch_memberships membership ON membership.id=ord.membership_id WHERE ord.id=$1),
 events AS (
 SELECT generated_at AS occurred_at,'card.activated' AS action,'activated' AS result,'{}'::jsonb AS details,'owner' AS origin,'card:'||bound_card_id AS sort_id FROM facts
 UNION ALL SELECT created_at,'first_claim','claimed','{}'::jsonb,'customer','order:'||id FROM facts
 UNION ALL SELECT version.created_at,'delivery_version_saved','published',jsonb_build_object('generation',version.generation),'system','version:'||version.id
 FROM facts JOIN tsw_delivery_versions version ON version.oauth_asset_id=facts.oauth_asset_id
 UNION ALL SELECT removed_at,'service_ended','ended','{}'::jsonb,'owner','membership:'||membership_id FROM facts WHERE removed_at IS NOT NULL
 UNION ALL SELECT event.occurred_at,COALESCE(NULLIF(event.details->>'action',''),event.event_type),COALESCE(NULLIF(event.details->>'result',''),event.outcome),event.details,event.actor_type,event.id::text
 FROM tsw_audit_events event,facts WHERE
 (event.entity_type='card' AND event.entity_id=facts.bound_card_id OR event.retention_scope_type='card' AND event.retention_scope_id=facts.bound_card_id OR event.entity_type='oauth_asset' AND event.entity_id=facts.oauth_asset_id
 OR event.entity_type='oauth_attempt' AND event.entity_id IN (SELECT id FROM tsw_oauth_attempts WHERE oauth_asset_id=facts.oauth_asset_id))
 AND event.event_type IN ('owner.card_revoked','oauth.reclaim_authorized','oauth.attempt_started','oauth.attempt_settled',
 'public.order_restored','public.reclaim_requested','public.reclaim_updated','public.status_changed','public.download_authorized'))`

func redemptionTimeline(ctx context.Context, tx pgx.Tx, orderID uuid.UUID, page, size int) (ownerapi.RedemptionTimelineList, error) {
	v := ownerapi.RedemptionTimelineList{Items: []ownerapi.DeliveryRecordEvent{}, Page: page, PageSize: ownerapi.RedemptionTimelineListPageSize(size)}
	if err := tx.QueryRow(ctx, redemptionTimelineSQL+` SELECT count(*) FROM events`, orderID).Scan(&v.Total); err != nil {
		return v, err
	}
	rows, err := tx.Query(ctx, redemptionTimelineSQL+` SELECT occurred_at,action,result,details,origin FROM events ORDER BY occurred_at DESC,sort_id DESC LIMIT $2 OFFSET $3`, orderID, size, (page-1)*size)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var item ownerapi.DeliveryRecordEvent
		var raw []byte
		var origin string
		if err = rows.Scan(&item.OccurredAt, &item.Action, &item.Result, &raw, &origin); err != nil {
			return v, err
		}
		var details struct {
			Generation *int64  `json:"generation"`
			Stage      *string `json:"stage"`
			Status     *string `json:"status"`
			Reason     *string `json:"reason"`
		}
		if err = json.Unmarshal(raw, &details); err != nil {
			return v, err
		}
		item.Generation = details.Generation
		item.Stage = details.Stage
		item.Status = details.Status
		item.Reason = details.Reason
		if origin == "anonymous" {
			origin = "customer"
		}
		if stringValue(details.Reason) == "authoritative_401" {
			origin = "automatic_401"
		}
		item.Origin = &origin
		v.Items = append(v.Items, item)
	}
	return v, rows.Err()
}
