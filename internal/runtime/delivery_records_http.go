package runtime

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/audit"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

type deliveryRecordScan struct {
	MotherAccountID    string
	SourceBatchName    *string
	CardState          ownerapi.CardState
	CardVersion        *int64
	SecretAvailable    bool
	MembershipID       string
	TargetAccountID    string
	TargetIdentifier   string
	WorkspaceID        string
	BatchID            string
	BatchSequenceNo    int64
	BatchStatus        string
	WorkspaceName      string
	AssetStatus        string
	Generation         int64
	LivenessStatus     *string
	LivenessOrigin     *string
	LivenessHTTPStatus *int
	LivenessErrorCode  *string
	ProbedAt           *time.Time
	CardStatus         *string
	CardSuffix         *string
	CardGeneratedAt    *time.Time
	RedemptionDeadline *time.Time
	ReclaimStatus      *string
	ReclaimTier        *string
	ReclaimResult      *string
	CardID             *string
	AssetID            string
	MembershipState    string
	OrderID            *string
	RedeemedAt         *time.Time
}

func (h *OwnerAuthHandler) ListDeliveryRecords(w http.ResponseWriter, r *http.Request, params ownerapi.ListDeliveryRecordsParams) {
	if _, ok := h.authenticated(w, r, false); !ok {
		return
	}
	if !validDeliveryRecordFilters(params) {
		writeProblem(w, r, http.StatusBadRequest, "invalid_delivery_filter", "Invalid Request", "A delivery filter is invalid", 0)
		return
	}
	page, pageSize, valid := pagination(params.Page, params.PageSize)
	if !valid {
		writeProblem(w, r, 400, "invalid_pagination", "Invalid Request", "Pagination is invalid", 0)
		return
	}
	search := strings.TrimSpace(stringValue(params.Search))
	if len(search) > 254 {
		writeProblem(w, r, 400, "invalid_delivery_search", "Invalid Request", "Search is too long", 0)
		return
	}
	if params.CardsOnly != nil && *params.CardsOnly && (params.MotherAccountId == nil || params.WorkspaceId == nil) {
		writeProblem(w, r, 400, "card_scope_required", "Invalid Request", "Choose a mother and workspace", 0)
		return
	}
	tx, err := h.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	args := deliveryRecordFilterArgs(params)
	var total int
	if err := tx.QueryRow(r.Context(), `SELECT count(*)`+deliveryRecordJoins+deliveryRecordFilterSQL, args...).Scan(&total); err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	queryArgs := append(append([]any(nil), args...), pageSize, (page-1)*pageSize)
	sortTime := "COALESCE(ord.created_at,card.created_at,membership.created_at)"
	if params.CardsOnly != nil && *params.CardsOnly {
		sortTime = "card.created_at"
	}
	rows, err := tx.Query(r.Context(), deliveryRecordQuery+deliveryRecordFilterSQL+` ORDER BY `+sortTime+` DESC,membership.id DESC LIMIT $11 OFFSET $12`, queryArgs...)
	if err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	defer rows.Close()
	items := make([]ownerapi.DeliveryRecord, 0, pageSize)
	for rows.Next() {
		scan, err := scanDeliveryRecord(rows)
		if err != nil {
			h.deliveryFailure(w, r, err)
			return
		}
		item, err := deliveryRecordFromScan(scan)
		if err != nil {
			h.deliveryFailure(w, r, err)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	rows.Close()
	if err := tx.Commit(r.Context()); err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ownerapi.DeliveryRecordList{Page: page, PageSize: pageSize, Total: total, Items: items})
}

func (h *OwnerAuthHandler) GetDeliveryRecord(w http.ResponseWriter, r *http.Request, membershipID ownerapi.MembershipId) {
	if _, ok := h.authenticated(w, r, false); !ok {
		return
	}
	scan, err := scanDeliveryRecord(h.pool.QueryRow(r.Context(), deliveryRecordQuery+` WHERE membership.id=$1`, uuid.UUID(membershipID)))
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, http.StatusNotFound, "delivery_not_found", "Not Found", "The delivery record was not found", 0)
		return
	}
	if err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	item, err := deliveryRecordFromScan(scan)
	if err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	events, err := h.deliveryRecordTimeline(r, scan)
	if err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	item.Timeline = &events
	writeJSON(w, http.StatusOK, item)
}

// This projection and filter use the same eligibility facts as redeemFacts.canClaim.
const deliveryCardStateSQL = `(CASE WHEN card.id IS NULL THEN 'unactivated'
 WHEN card.status='revoked' THEN 'revoked' WHEN ord.id IS NOT NULL THEN 'claimed'
 WHEN card.redemption_deadline<=now() OR batch.planned_at<=now() THEN 'expired'
 WHEN membership.state<>'active' OR batch.status<>'serving' OR asset.status<>'ready'
   OR credential.material_status IS DISTINCT FROM 'complete' OR asset.current_delivery_version_id IS NULL THEN 'unavailable'
 ELSE 'unclaimed' END)`
const deliveryRecordFilterSQL = ` WHERE ($1::text IS NULL OR (CASE WHEN membership.state='active' THEN 'active' ELSE 'ended' END)=$1::text)
 AND ($2::text IS NULL OR COALESCE(card.status,'unactivated')=$2::text)
 AND ($3::text IS NULL OR (CASE WHEN ord.id IS NULL THEN 'unclaimed' ELSE 'claimed' END)=$3::text)
 AND ($4::text IS NULL OR target.identifier ILIKE '%'||$4||'%' OR workspace.display_name ILIKE '%'||$4||'%' OR card.display_suffix ILIKE '%'||$4||'%' OR batch.source_batch_name ILIKE '%'||$4||'%')
 AND ($5::uuid IS NULL OR batch.id=$5)
 AND ($6::uuid IS NULL OR binding.mother_account_id=$6)
 AND ($7::uuid IS NULL OR workspace.id=$7)
 AND ($8::uuid IS NULL OR membership.id=$8)
 AND (NOT $9::boolean OR card.id IS NOT NULL)
 AND ($10::text IS NULL OR ` + deliveryCardStateSQL + `=$10::text)`

func validDeliveryRecordFilters(params ownerapi.ListDeliveryRecordsParams) bool {
	return (params.ServiceStatus == nil || params.ServiceStatus.Valid()) &&
		(params.CardStatus == nil || params.CardStatus.Valid()) &&
		(params.OrderStatus == nil || params.OrderStatus.Valid()) &&
		(params.CardState == nil || params.CardState.Valid())
}
func deliveryRecordFilterArgs(params ownerapi.ListDeliveryRecordsParams) []any {
	return []any{params.ServiceStatus, params.CardStatus, params.OrderStatus, nullableDeliverySearch(strings.TrimSpace(stringValue(params.Search))), params.BatchId, params.MotherAccountId, params.WorkspaceId, params.MembershipId, params.CardsOnly != nil && *params.CardsOnly, params.CardState}
}
func nullableDeliverySearch(search string) any {
	if search == "" {
		return nil
	}
	return search
}

const deliveryRecordJoins = ` FROM tsw_batch_memberships membership
 JOIN tsw_batches batch ON batch.id=membership.batch_id
 JOIN tsw_target_accounts target ON target.id=membership.target_account_id
 JOIN tsw_mother_workspace_bindings binding ON binding.id=batch.binding_id
 JOIN tsw_workspaces workspace ON workspace.id=binding.workspace_id
 JOIN tsw_oauth_assets asset ON asset.membership_id=membership.id
 LEFT JOIN tsw_target_credentials credential ON credential.target_account_id=target.id
 LEFT JOIN tsw_cards card ON card.membership_id=membership.id
 LEFT JOIN tsw_orders ord ON ord.membership_id=membership.id`
const deliveryRecordQuery = `SELECT membership.id::text,membership.target_account_id::text,target.identifier,workspace.id::text,batch.id::text,batch.sequence_no,batch.status,workspace.display_name,
 asset.id::text,asset.status,asset.current_generation,asset.liveness_status,asset.liveness_origin,asset.liveness_http_status,
 asset.liveness_error_code,asset.probed_at,card.id::text,card.status,card.display_suffix,card.created_at,COALESCE(card.redemption_deadline,batch.planned_at),
 reclaim.status,reclaim.reclaim_tier,reclaim.reclaim_result,membership.state,ord.id::text,ord.created_at,
 binding.mother_account_id::text,batch.source_batch_name,` + deliveryCardStateSQL + `,card.version,card.sealed_secret IS NOT NULL` + deliveryRecordJoins + `
 LEFT JOIN LATERAL (
 SELECT task.status,task.reclaim_tier,task.reclaim_result FROM tsw_tasks task
 WHERE task.task_type='oauth_reclaim' AND task.oauth_asset_id=asset.id ORDER BY task.created_at DESC LIMIT 1
 ) reclaim ON TRUE`

func scanDeliveryRecord(row interface{ Scan(...any) error }) (deliveryRecordScan, error) {
	var scan deliveryRecordScan
	err := row.Scan(&scan.MembershipID, &scan.TargetAccountID, &scan.TargetIdentifier, &scan.WorkspaceID, &scan.BatchID, &scan.BatchSequenceNo, &scan.BatchStatus, &scan.WorkspaceName,
		&scan.AssetID, &scan.AssetStatus, &scan.Generation, &scan.LivenessStatus, &scan.LivenessOrigin, &scan.LivenessHTTPStatus, &scan.LivenessErrorCode, &scan.ProbedAt, &scan.CardID, &scan.CardStatus,
		&scan.CardSuffix, &scan.CardGeneratedAt, &scan.RedemptionDeadline, &scan.ReclaimStatus, &scan.ReclaimTier, &scan.ReclaimResult, &scan.MembershipState, &scan.OrderID, &scan.RedeemedAt,
		&scan.MotherAccountID, &scan.SourceBatchName, &scan.CardState, &scan.CardVersion, &scan.SecretAvailable)
	return scan, err
}

func deliveryRecordFromScan(scan deliveryRecordScan) (ownerapi.DeliveryRecord, error) {
	membershipID, err := uuid.Parse(scan.MembershipID)
	if err != nil {
		return ownerapi.DeliveryRecord{}, err
	}
	targetID, err := uuid.Parse(scan.TargetAccountID)
	if err != nil {
		return ownerapi.DeliveryRecord{}, err
	}
	workspaceID, err := uuid.Parse(scan.WorkspaceID)
	if err != nil {
		return ownerapi.DeliveryRecord{}, err
	}
	batchID, err := uuid.Parse(scan.BatchID)
	if err != nil {
		return ownerapi.DeliveryRecord{}, err
	}
	serviceStatus := ownerapi.DeliveryRecordServiceStatus("ended")
	if scan.MembershipState == "active" {
		serviceStatus = ownerapi.DeliveryRecordServiceStatus("active")
	}
	orderStatus := ownerapi.DeliveryRecordOrderStatus("unclaimed")
	if scan.OrderID != nil {
		orderStatus = ownerapi.DeliveryRecordOrderStatus("claimed")
	}
	cardStatus := "unactivated"
	if scan.CardStatus != nil {
		cardStatus = *scan.CardStatus
	}
	return ownerapi.DeliveryRecord{
		MotherAccountId: uuid.MustParse(scan.MotherAccountID), SourceBatchName: scan.SourceBatchName, CardState: scan.CardState, CardVersion: scan.CardVersion, SecretAvailable: scan.SecretAvailable,
		MembershipId: membershipID, TargetAccountId: targetID, TargetIdentifier: scan.TargetIdentifier, WorkspaceId: workspaceID, BatchId: batchID,
		BatchSequenceNo: scan.BatchSequenceNo, BatchStatus: ownerapi.BatchStatus(scan.BatchStatus), WorkspaceName: scan.WorkspaceName, AssetStatus: scan.AssetStatus, Generation: scan.Generation,
		LivenessStatus: scan.LivenessStatus, LivenessOrigin: scan.LivenessOrigin, LivenessHttpStatus: scan.LivenessHTTPStatus,
		LivenessErrorCode: scan.LivenessErrorCode, ProbedAt: scan.ProbedAt, CardStatus: &cardStatus,
		CardDisplaySuffix: scan.CardSuffix, CardGeneratedAt: scan.CardGeneratedAt, RedemptionDeadline: scan.RedemptionDeadline, ReclaimStatus: scan.ReclaimStatus,
		ReclaimTier: scan.ReclaimTier, ReclaimResult: scan.ReclaimResult, RedeemedAt: scan.RedeemedAt,
		ServiceStatus: &serviceStatus, OrderStatus: &orderStatus,
	}, nil
}

func (h *OwnerAuthHandler) deliveryRecordTimeline(r *http.Request, scan deliveryRecordScan) ([]ownerapi.DeliveryRecordEvent, error) {
	if scan.CardID == nil {
		return []ownerapi.DeliveryRecordEvent{}, nil
	}
	rows, err := h.pool.Query(r.Context(), `SELECT occurred_at,event_type,outcome,details
		FROM tsw_audit_events
		WHERE (retention_scope_type='card' AND retention_scope_id=$1::uuid)
           OR (entity_type='card' AND entity_id=$1)
		   OR (entity_type='oauth_asset' AND entity_id=$2)
		   OR (entity_type='oauth_attempt' AND entity_id IN (SELECT id FROM tsw_oauth_attempts WHERE oauth_asset_id=$2::uuid))
		ORDER BY occurred_at DESC,id DESC LIMIT 100`, *scan.CardID, scan.AssetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]ownerapi.DeliveryRecordEvent, 0, 16)
	for rows.Next() {
		var occurredAt time.Time
		var eventType, outcome string
		var raw []byte
		if err := rows.Scan(&occurredAt, &eventType, &outcome, &raw); err != nil {
			return nil, err
		}
		var detail map[string]any
		_ = json.Unmarshal(raw, &detail)
		event := ownerapi.DeliveryRecordEvent{OccurredAt: occurredAt, Action: strings.TrimPrefix(eventType, "public."), Result: outcome}
		if value, ok := detail["action"].(string); ok && value != "" {
			event.Action = value
		}
		if value, ok := detail["result"].(string); ok && value != "" {
			event.Result = value
		}
		if value, ok := detail["reason"].(string); ok && value != "" {
			event.Reason = &value
		}
		if value, ok := detail["status"].(string); ok && value != "" {
			event.Status = &value
		}
		if value, ok := detail["http_status"].(float64); ok && value >= 100 && value <= 599 {
			httpStatus := int(value)
			event.HttpStatus = &httpStatus
		}
		if value, ok := detail["result"].(string); ok && (value == "probe_ok" || value == "token_refresh" || value == "full_relogin" || value == "unrecoverable") {
			event.Tier = &value
		}
		if strings.HasPrefix(eventType, "public.reclaim_requested") {
			origin := "customer"
			event.Origin = &origin
		} else if eventType == string(audit.OwnerDeliveryReclaimAuthorized) || eventType == string(audit.OwnerCardRevoked) {
			origin := "owner"
			event.Origin = &origin
		} else if value, ok := detail["reason"].(string); ok && value == "authoritative_401" {
			origin := "automatic"
			event.Origin = &origin
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
