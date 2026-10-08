package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	targetdomain "github.com/xft0202/Apophis-TeamSeatWatch/internal/target"
)

const standbyChildBatchLimit = 10000

func standbyBatchRecord(r *http.Request, tx interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id uuid.UUID) (ownerapi.StandbyChildBatch, error) {
	var item ownerapi.StandbyChildBatch
	err := tx.QueryRow(r.Context(), `SELECT b.id,b.name,b.version,b.updated_at,count(m.target_account_id),count(DISTINCT nullif(split_part(a.identifier,'@',2),'')),
 coalesce(array_agg(DISTINCT split_part(a.identifier,'@',2) ORDER BY split_part(a.identifier,'@',2)) FILTER (WHERE a.id IS NOT NULL AND split_part(a.identifier,'@',2)<>''), ARRAY[]::text[])
 FROM tsw_standby_child_batches b LEFT JOIN tsw_standby_child_memberships m ON m.batch_id=b.id
 LEFT JOIN tsw_target_accounts a ON a.id=m.target_account_id WHERE b.id=$1 AND b.deleted_at IS NULL GROUP BY b.id`, id).Scan(&item.Id, &item.Name, &item.Version, &item.UpdatedAt, &item.MemberCount, &item.DomainCount, &item.Domains)
	return item, err
}

func (h *OwnerAuthHandler) ListStandbyChildBatches(w http.ResponseWriter, r *http.Request, params ownerapi.ListStandbyChildBatchesParams) {
	if _, ok := h.authenticated(w, r, false); !ok {
		return
	}
	page, size, ok := pagination(params.Page, params.PageSize)
	search := strings.TrimSpace(stringValue(params.Search))
	domain := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(stringValue(params.Domain))), "@")
	if !ok || len(search) > 254 || len(domain) > 254 || strings.ContainsAny(domain, " @\t\r\n") {
		writeProblem(w, r, 400, "invalid_filter", "Invalid Request", "Invalid batch pagination or filter", 0)
		return
	}
	where := ` WHERE b.deleted_at IS NULL AND ($1='' OR b.name ILIKE '%'||$1||'%') AND ($2='' OR EXISTS (
 SELECT 1 FROM tsw_standby_child_memberships m JOIN tsw_target_accounts a ON a.id=m.target_account_id
 WHERE m.batch_id=b.id AND split_part(a.identifier,'@',2)=$2)) `
	result := ownerapi.StandbyChildBatchList{Items: []ownerapi.StandbyChildBatch{}, Page: page, PageSize: size}
	if err := h.pool.QueryRow(r.Context(), `SELECT count(*) FROM tsw_standby_child_batches b`+where, search, domain).Scan(&result.Total); err != nil {
		h.targetFailure(w, r, err)
		return
	}
	rows, err := h.pool.Query(r.Context(), `WITH page_batches AS MATERIALIZED (
 SELECT b.* FROM tsw_standby_child_batches b`+where+` ORDER BY b.updated_at DESC,b.id LIMIT $3 OFFSET $4)
 SELECT b.id,b.name,b.version,b.updated_at,count(m.target_account_id),count(DISTINCT nullif(split_part(a.identifier,'@',2),'')),
 coalesce(array_agg(DISTINCT split_part(a.identifier,'@',2) ORDER BY split_part(a.identifier,'@',2)) FILTER (WHERE a.id IS NOT NULL AND split_part(a.identifier,'@',2)<>''), ARRAY[]::text[])
 FROM page_batches b LEFT JOIN tsw_standby_child_memberships m ON m.batch_id=b.id
 LEFT JOIN tsw_target_accounts a ON a.id=m.target_account_id
 GROUP BY b.id,b.name,b.version,b.updated_at ORDER BY b.updated_at DESC,b.id`, search, domain, size, (page-1)*size)
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var item ownerapi.StandbyChildBatch
		if err = rows.Scan(&item.Id, &item.Name, &item.Version, &item.UpdatedAt, &item.MemberCount, &item.DomainCount, &item.Domains); err != nil {
			h.targetFailure(w, r, err)
			return
		}
		result.Items = append(result.Items, item)
	}
	if err = rows.Err(); err != nil {
		h.targetFailure(w, r, err)
		return
	}
	writeJSON(w, 200, result)
}

func (h *OwnerAuthHandler) GetStandbyChildBatch(w http.ResponseWriter, r *http.Request, id ownerapi.StandbyBatchId) {
	if _, ok := h.authenticated(w, r, false); !ok {
		return
	}
	item, err := standbyBatchRecord(r, h.pool, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "batch_not_found", "Not Found", "Batch not found", 0)
		return
	}
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	writeJSON(w, 200, item)
}

func (h *OwnerAuthHandler) RenameStandbyChildBatch(w http.ResponseWriter, r *http.Request, id ownerapi.StandbyBatchId, _ ownerapi.RenameStandbyChildBatchParams) {
	if _, ok := h.authenticated(w, r, true); !ok {
		return
	}
	var request ownerapi.StandbyChildBatchRename
	if !decodeJSON(w, r, &request) || !validLength(strings.TrimSpace(request.Name), 1, 120) || request.ExpectedVersion < 1 {
		writeProblem(w, r, 422, "invalid_batch", "Invalid Batch", "Invalid batch name", 0)
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var version int64
	err = tx.QueryRow(r.Context(), `SELECT version FROM tsw_standby_child_batches WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&version)
	if err == nil && version != request.ExpectedVersion {
		err = errStandbyConflict
	}
	if err != nil {
		standbyError(h, w, r, err)
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE tsw_standby_child_batches SET name=$1,version=version+1,updated_at=now() WHERE id=$2 AND name IS DISTINCT FROM $1`, strings.TrimSpace(request.Name), id)
	var result ownerapi.StandbyChildBatch
	if err == nil {
		result, err = standbyBatchRecord(r, tx, id)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		standbyError(h, w, r, err)
		return
	}
	writeJSON(w, 200, result)
}

func (h *OwnerAuthHandler) DeleteStandbyChildBatch(w http.ResponseWriter, r *http.Request, id ownerapi.StandbyBatchId, _ ownerapi.DeleteStandbyChildBatchParams) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	request := ownerapi.StandbyChildBatchDelete{ExpectedCount: -1}
	if !decodeJSON(w, r, &request) || !request.Confirmed || request.ExpectedVersion < 1 || request.ExpectedCount < 0 {
		writeProblem(w, r, 422, "invalid_batch", "Invalid Batch", "Confirm the exact batch version and member count", 0)
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	// Discover members first, then use the account-before-batch lock order shared
	// by adds, transfers and exports. The batch version catches competing adds.
	rows, err := tx.Query(r.Context(), `SELECT target_account_id,version FROM tsw_standby_child_memberships WHERE batch_id=$1 ORDER BY target_account_id`, id)
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	selection := ownerapi.StandbyChildSelection{Scope: ownerapi.StandbyChildSelectionScopeBatch}
	for rows.Next() {
		var member ownerapi.StandbyChildSelectionMember
		if err = rows.Scan(&member.AccountId, &member.MembershipVersion); err != nil {
			break
		}
		selection.Members = append(selection.Members, member)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err == nil {
		_, err = lockStandbySelection(r, tx, selection)
	}
	if err != nil {
		standbyError(h, w, r, err)
		return
	}
	var version, count int64
	err = tx.QueryRow(r.Context(), `SELECT version FROM tsw_standby_child_batches WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "batch_not_found", "Not Found", "Batch not found", 0)
		return
	}
	if err == nil {
		err = tx.QueryRow(r.Context(), `SELECT count(*) FROM tsw_standby_child_memberships WHERE batch_id=$1`, id).Scan(&count)
	}
	if err == nil && (version != request.ExpectedVersion || count != request.ExpectedCount || count != int64(len(selection.Members))) {
		err = errStandbyConflict
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `WITH released AS (
 UPDATE tsw_standby_child_memberships SET batch_id=NULL,version=version+1 WHERE batch_id=$1 RETURNING target_account_id)
 INSERT INTO tsw_standby_child_history(target_account_id,previous_batch_id,batch_id,owner_id)
 SELECT target_account_id,$1,NULL,$2 FROM released`, id, owner.OwnerID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE tsw_standby_child_batches SET deleted_at=now(),deleted_by=$2,version=version+1,updated_at=now() WHERE id=$1`, id, owner.OwnerID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		standbyError(h, w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Preview resolves every page into one fixed set. The version is zero for an
// account never assigned to a standby batch; even removal retains its version.
func (h *OwnerAuthHandler) PreviewStandbyChildSelection(w http.ResponseWriter, r *http.Request, _ ownerapi.PreviewStandbyChildSelectionParams) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	var request ownerapi.StandbyChildSelectionRequest
	if !decodeJSON(w, r, &request) {
		writeProblem(w, r, 422, "invalid_selection", "Invalid Selection", "Invalid selection", 0)
		return
	}
	domain := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(stringValue(request.Domain))), "@")
	probe, membership := "", ""
	if request.ProbeStatus != nil {
		probe = string(*request.ProbeStatus)
	}
	if request.MembershipStatus != nil {
		membership = string(*request.MembershipStatus)
	}
	hasFilters := request.Domain != nil || request.ProbeStatus != nil || request.MembershipStatus != nil || request.ExcludeBatchId != nil
	query := `SELECT target.id,coalesce(m.version,0),count(*) OVER(),b.id,b.name
 FROM tsw_target_accounts target JOIN tsw_target_credentials credentials ON credentials.target_account_id=target.id
 LEFT JOIN tsw_standby_child_memberships m ON m.target_account_id=target.id
 LEFT JOIN tsw_standby_child_batches b ON b.id=m.batch_id `
	var args []any
	switch request.Scope {
	case ownerapi.StandbyChildSelectionRequestScopeSelected:
		if request.AccountIds == nil || request.Search != nil || request.BatchId != nil || hasFilters || len(*request.AccountIds) == 0 || len(*request.AccountIds) > standbyChildBatchLimit {
			writeProblem(w, r, 422, "invalid_selection", "Invalid Selection", "Invalid selection", 0)
			return
		}
		args = []any{*request.AccountIds}
		query += ` WHERE target.id=ANY($1) `
	case ownerapi.StandbyChildSelectionRequestScopeFiltered:
		search := strings.ToLower(strings.TrimSpace(stringValue(request.Search)))
		if request.AccountIds != nil || len(search) > 254 || len(domain) > 254 || strings.ContainsAny(domain, " @\t\r\n") || !validTargetProbeFilter(probe) || !validStandbyMembershipFilter(membership) {
			writeProblem(w, r, 422, "invalid_selection", "Invalid Selection", "Invalid selection", 0)
			return
		}
		args = []any{search, domain, probe, membership, request.ExcludeBatchId, owner.OwnerID, request.BatchId}
		query += targetListPersonalSQL + ` WHERE ($1='' OR target.identifier ILIKE '%'||$1||'%' OR target.display_label ILIKE '%'||$1||'%')
 AND ($2='' OR split_part(target.identifier,'@',2)=$2)
 AND ($3='' OR ($3='unprobed' AND COALESCE(personal.classification,credentials.latest_probe_status) IS NULL)
 OR COALESCE(personal.classification,credentials.latest_probe_status)=$3)
 AND ($4='' OR ($4='unassigned' AND m.batch_id IS NULL) OR ($4='assigned' AND m.batch_id IS NOT NULL))
 AND ($5::uuid IS NULL OR m.batch_id IS DISTINCT FROM $5) AND ($7::uuid IS NULL OR m.batch_id=$7) `
	case ownerapi.StandbyChildSelectionRequestScopeBatch:
		if request.AccountIds != nil || request.Search != nil || request.BatchId == nil || hasFilters {
			writeProblem(w, r, 422, "invalid_selection", "Invalid Selection", "Invalid selection", 0)
			return
		}
		args = []any{*request.BatchId}
		if _, err := standbyBatchRecord(r, h.pool, *request.BatchId); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeProblem(w, r, 404, "batch_not_found", "Not Found", "Batch not found", 0)
			} else {
				h.targetFailure(w, r, err)
			}
			return
		}
		query += ` WHERE m.batch_id=$1 `
	default:
		writeProblem(w, r, 422, "invalid_selection", "Invalid Selection", "Invalid selection", 0)
		return
	}
	rows, err := h.pool.Query(r.Context(), query+`ORDER BY target.id LIMIT 10001`, args...)
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	defer rows.Close()
	result := ownerapi.StandbyChildSelection{Scope: ownerapi.StandbyChildSelectionScope(request.Scope), Members: []ownerapi.StandbyChildSelectionMember{}}
	var actualCount int64
	for rows.Next() {
		var member ownerapi.StandbyChildSelectionMember
		var sourceID *uuid.UUID
		var sourceName *string
		if err = rows.Scan(&member.AccountId, &member.MembershipVersion, &actualCount, &sourceID, &sourceName); err != nil {
			h.targetFailure(w, r, err)
			return
		}
		if sourceID != nil && sourceName != nil {
			member.CurrentBatch = &ownerapi.StandbyChildBatchRef{Id: *sourceID, Name: *sourceName}
		}
		result.Members = append(result.Members, member)
	}
	if err = rows.Err(); err != nil {
		h.targetFailure(w, r, err)
		return
	}
	if actualCount > standbyChildBatchLimit {
		writeStandbyRangeLimit(w, r, actualCount)
		return
	}
	if request.Scope == ownerapi.StandbyChildSelectionRequestScopeSelected && len(result.Members) != len(*request.AccountIds) {
		writeProblem(w, r, 409, "selection_changed", "Selection Changed", "Refresh the selection", 0)
		return
	}
	result.Count = len(result.Members)
	writeJSON(w, 200, result)
}

// A 10000-member selection with source labels can exceed the ordinary JSON cap.
func decodeStandbyChange(w http.ResponseWriter, r *http.Request, value any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil {
		return false
	}
	var trailing any
	return errors.Is(decoder.Decode(&trailing), io.EOF)
}

func validStandbySelection(selection ownerapi.StandbyChildSelection, expected int) bool {
	if expected != selection.Count || expected != len(selection.Members) || expected < 0 || expected > standbyChildBatchLimit || !selection.Scope.Valid() {
		return false
	}
	seen := make(map[uuid.UUID]bool, len(selection.Members))
	for _, m := range selection.Members {
		if m.AccountId == uuid.Nil || m.MembershipVersion < 0 || seen[m.AccountId] {
			return false
		}
		seen[m.AccountId] = true
	}
	return true
}

// All operations lock canonical account rows in UUID order before reading the
// assignment, so an absent membership row cannot race another insertion.
func lockStandbySelection(r *http.Request, tx pgx.Tx, selection ownerapi.StandbyChildSelection) (map[uuid.UUID]*uuid.UUID, error) {
	members := append([]ownerapi.StandbyChildSelectionMember{}, selection.Members...)
	sort.Slice(members, func(i, j int) bool { return members[i].AccountId.String() < members[j].AccountId.String() })
	current := make(map[uuid.UUID]*uuid.UUID, len(members))
	for _, member := range members {
		var id uuid.UUID
		if err := tx.QueryRow(r.Context(), `SELECT id FROM tsw_target_accounts WHERE id=$1 FOR UPDATE`, member.AccountId).Scan(&id); err != nil {
			return nil, err
		}
		var batch *uuid.UUID
		var version int64
		err := tx.QueryRow(r.Context(), `SELECT batch_id,version FROM tsw_standby_child_memberships WHERE target_account_id=$1`, id).Scan(&batch, &version)
		if errors.Is(err, pgx.ErrNoRows) {
			version = 0
			err = nil
		}
		if err != nil {
			return nil, err
		}
		if version != member.MembershipVersion {
			return nil, errStandbyConflict
		}
		current[id] = batch
	}
	return current, nil
}

var errStandbyConflict = errors.New("standby selection or batch changed")

func writeStandbyRangeLimit(w http.ResponseWriter, r *http.Request, actualCount int64) {
	const code = "range_limit_exceeded"
	detail := fmt.Sprintf("Scope contains %d accounts; a standby batch supports at most %d. Narrow the filter or selection.", actualCount, standbyChildBatchLimit)
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(ownerapi.Problem{
		Type: "urn:teamseatwatch:problem:" + code, Title: "Range Limit Exceeded", Status: http.StatusConflict,
		Code: code, RequestId: requestID(r), Detail: &detail, ActualCount: &actualCount,
	})
}

func standbyError(h *OwnerAuthHandler, w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errStandbyConflict) || errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 409, "selection_changed", "Selection Changed", "Refresh and confirm the exact selection", 0)
		return
	}
	h.targetFailure(w, r, err)
}

func (h *OwnerAuthHandler) CreateStandbyChildBatch(w http.ResponseWriter, r *http.Request, _ ownerapi.CreateStandbyChildBatchParams) {
	h.changeStandbyBatch(w, r, uuid.Nil, false)
}
func (h *OwnerAuthHandler) UpdateStandbyChildBatch(w http.ResponseWriter, r *http.Request, id ownerapi.StandbyBatchId, _ ownerapi.UpdateStandbyChildBatchParams) {
	h.changeStandbyBatch(w, r, id, true)
}
func (h *OwnerAuthHandler) changeStandbyBatch(w http.ResponseWriter, r *http.Request, id uuid.UUID, existing bool) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	var request ownerapi.StandbyChildBatchChange
	if !decodeStandbyChange(w, r, &request) || !validLength(strings.TrimSpace(request.Name), 1, 120) || !request.Confirmed || !validStandbySelection(request.Selection, request.ExpectedCount) || existing && (request.ExpectedVersion == nil || *request.ExpectedVersion < 1) || !existing && (request.ExpectedVersion != nil || request.Action != nil) || request.Action != nil && !request.Action.Valid() {
		writeProblem(w, r, 422, "invalid_batch", "Invalid Batch", "Confirm the exact batch selection", 0)
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	current, err := lockStandbySelection(r, tx, request.Selection)
	if err != nil {
		standbyError(h, w, r, err)
		return
	}
	// Lock all affected batch versions in one order, including transfer sources.
	batchIDs := map[uuid.UUID]bool{}
	if existing {
		batchIDs[id] = true
	}
	for _, previous := range current {
		if previous != nil {
			batchIDs[*previous] = true
		}
	}
	ordered := make([]uuid.UUID, 0, len(batchIDs))
	for batchID := range batchIDs {
		ordered = append(ordered, batchID)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].String() < ordered[j].String() })
	for _, batchID := range ordered {
		var locked uuid.UUID
		if err = tx.QueryRow(r.Context(), `SELECT id FROM tsw_standby_child_batches WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, batchID).Scan(&locked); err != nil {
			standbyError(h, w, r, err)
			return
		}
	}
	name := strings.TrimSpace(request.Name)
	if !existing {
		err = tx.QueryRow(r.Context(), `INSERT INTO tsw_standby_child_batches(name) VALUES($1) RETURNING id`, name).Scan(&id)
	} else {
		var version int64
		var currentName string
		err = tx.QueryRow(r.Context(), `SELECT version,name FROM tsw_standby_child_batches WHERE id=$1`, id).Scan(&version, &currentName)
		if err == nil && (version != *request.ExpectedVersion || currentName != name) {
			err = errStandbyConflict
		}
	}
	if err != nil {
		standbyError(h, w, r, err)
		return
	}
	action := ownerapi.Add
	if request.Action != nil {
		action = *request.Action
	}
	if action == ownerapi.Add {
		// Every supported writer holds the destination batch row before changing
		// memberships. The count therefore includes committed competing additions.
		var count int64
		if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM tsw_standby_child_memberships WHERE batch_id=$1`, id).Scan(&count); err != nil {
			standbyError(h, w, r, err)
			return
		}
		for _, previous := range current {
			if previous == nil || *previous != id {
				count++
			}
		}
		if count > standbyChildBatchLimit {
			writeStandbyRangeLimit(w, r, count)
			return
		}
	}
	changed := false
	for _, member := range request.Selection.Members {
		previous := current[member.AccountId]
		if action == ownerapi.Remove && (previous == nil || *previous != id) {
			err = errStandbyConflict
			break
		}
		next := &id
		if action == ownerapi.Remove {
			next = nil
		}
		if previous != nil && next != nil && *previous == *next {
			continue
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO tsw_standby_child_memberships(target_account_id,batch_id) VALUES($1,$2)
   ON CONFLICT (target_account_id) DO UPDATE SET batch_id=EXCLUDED.batch_id,version=tsw_standby_child_memberships.version+1`, member.AccountId, next)
		if err != nil {
			break
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO tsw_standby_child_history(target_account_id,previous_batch_id,batch_id,owner_id) VALUES($1,$2,$3,$4)`, member.AccountId, previous, next, owner.OwnerID)
		if err != nil {
			break
		}
		changed = true
		if previous != nil && *previous != id {
			_, err = tx.Exec(r.Context(), `UPDATE tsw_standby_child_batches SET version=version+1,updated_at=now() WHERE id=$1`, previous)
			if err != nil {
				break
			}
		}
	}
	// Membership edits never rename a batch. The dedicated name endpoint owns
	// that capability; same-batch no-ops preserve the version and update time.
	if err == nil && existing && changed {
		tag, updateErr := tx.Exec(r.Context(), `UPDATE tsw_standby_child_batches SET version=version+1,updated_at=now() WHERE id=$1 AND version=$2`, id, *request.ExpectedVersion)
		err = updateErr
		if err == nil && tag.RowsAffected() != 1 {
			err = errStandbyConflict
		}
	}

	if err != nil {
		standbyError(h, w, r, err)
		return
	}
	result, err := standbyBatchRecord(r, tx, id)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		standbyError(h, w, r, err)
		return
	}
	if existing {
		writeJSON(w, 200, result)
	} else {
		writeJSON(w, 201, result)
	}
}

func (h *OwnerAuthHandler) ExportStandbyChildBatch(w http.ResponseWriter, r *http.Request, id ownerapi.StandbyBatchId, _ ownerapi.ExportStandbyChildBatchParams) {
	if _, ok := h.authenticated(w, r, true); !ok {
		return
	}
	var request ownerapi.StandbyChildBatchExport
	if !decodeStandbyChange(w, r, &request) || !request.Confirmed || request.Selection.Scope != ownerapi.StandbyChildSelectionScopeBatch || !validStandbySelection(request.Selection, request.ExpectedCount) || request.ExpectedVersion == nil || *request.ExpectedVersion < 1 {
		writeProblem(w, r, 422, "invalid_export", "Invalid Export", "Confirm the exact batch count", 0)
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	current, err := lockStandbySelection(r, tx, request.Selection)
	if err != nil {
		standbyError(h, w, r, err)
		return
	}
	for _, member := range request.Selection.Members {
		batch := current[member.AccountId]
		if batch == nil || *batch != id {
			standbyError(h, w, r, errStandbyConflict)
			return
		}
	}
	var version, count int64
	err = tx.QueryRow(r.Context(), `SELECT version FROM tsw_standby_child_batches WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&version)
	if err == nil {
		err = tx.QueryRow(r.Context(), `SELECT count(*) FROM tsw_standby_child_memberships WHERE batch_id=$1`, id).Scan(&count)
	}
	if err != nil {
		standbyError(h, w, r, err)
		return
	}
	if version != *request.ExpectedVersion || count != int64(request.ExpectedCount) || count == 0 {
		standbyError(h, w, r, errStandbyConflict)
		return
	}
	members := append([]ownerapi.StandbyChildSelectionMember{}, request.Selection.Members...)
	sort.Slice(members, func(i, j int) bool { return members[i].AccountId.String() < members[j].AccountId.String() })
	var lines []string
	for _, member := range members {
		var identifier string
		var password, totp []byte
		err = tx.QueryRow(r.Context(), `SELECT a.identifier,c.password_secret,c.totp_secret FROM tsw_target_accounts a JOIN tsw_target_credentials c ON c.target_account_id=a.id WHERE a.id=$1`, member.AccountId).Scan(&identifier, &password, &totp)
		if err != nil {
			break
		}
		var pass, secret string
		pass, err = targetdomain.OpenMaterial(password, h.keyRing)
		if err != nil {
			break
		}
		secret, err = targetdomain.OpenMaterial(totp, h.keyRing)
		if err != nil {
			break
		}
		if strings.Contains(pass, "----") || strings.Contains(secret, "----") || strings.ContainsAny(pass+secret, "\r\n") {
			err = errors.New("unrepresentable TXT material")
			break
		}
		lines = append(lines, fmt.Sprintf("%s----%s----%s\n", identifier, pass, secret))
	}
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		h.targetFailure(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="child-account-materials.txt"`)
	w.WriteHeader(200)
	_, _ = w.Write([]byte(strings.Join(lines, "")))
}
