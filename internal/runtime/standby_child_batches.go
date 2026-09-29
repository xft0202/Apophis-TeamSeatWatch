package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	targetdomain "github.com/xft0202/Apophis-TeamSeatWatch/internal/target"
)

func standbyBatchTx(r *http.Request, tx pgx.Tx, id uuid.UUID) (ownerapi.StandbyChildBatch, error) {
	var item ownerapi.StandbyChildBatch
	err := tx.QueryRow(r.Context(), `SELECT b.id,b.name,b.version,count(m.target_account_id),count(DISTINCT split_part(a.identifier,'@',2)),
 coalesce(array_agg(DISTINCT split_part(a.identifier,'@',2) ORDER BY split_part(a.identifier,'@',2)) FILTER (WHERE a.id IS NOT NULL), ARRAY[]::text[])
 FROM tsw_standby_child_batches b LEFT JOIN tsw_standby_child_memberships m ON m.batch_id=b.id
 LEFT JOIN tsw_target_accounts a ON a.id=m.target_account_id WHERE b.id=$1 GROUP BY b.id`, id).Scan(&item.Id, &item.Name, &item.Version, &item.MemberCount, &item.DomainCount, &item.Domains)
	return item, err
}

func (h *OwnerAuthHandler) ListStandbyChildBatches(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticated(w, r, false); !ok {
		return
	}
	rows, err := h.pool.Query(r.Context(), `SELECT b.id,b.name,b.version,count(m.target_account_id),count(DISTINCT split_part(a.identifier,'@',2)),
 coalesce(array_agg(DISTINCT split_part(a.identifier,'@',2) ORDER BY split_part(a.identifier,'@',2)) FILTER (WHERE a.id IS NOT NULL), ARRAY[]::text[])
 FROM tsw_standby_child_batches b LEFT JOIN tsw_standby_child_memberships m ON m.batch_id=b.id
 LEFT JOIN tsw_target_accounts a ON a.id=m.target_account_id GROUP BY b.id ORDER BY b.created_at DESC,b.id`)
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	defer rows.Close()
	items := []ownerapi.StandbyChildBatch{}
	for rows.Next() {
		var item ownerapi.StandbyChildBatch
		if err = rows.Scan(&item.Id, &item.Name, &item.Version, &item.MemberCount, &item.DomainCount, &item.Domains); err != nil {
			h.targetFailure(w, r, err)
			return
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		h.targetFailure(w, r, err)
		return
	}
	writeJSON(w, 200, items)
}

// Preview resolves every page into one fixed set. The version is zero for an
// account never assigned to a standby batch; even removal retains its version.
func (h *OwnerAuthHandler) PreviewStandbyChildSelection(w http.ResponseWriter, r *http.Request, _ ownerapi.PreviewStandbyChildSelectionParams) {
	if _, ok := h.authenticated(w, r, true); !ok {
		return
	}
	var request ownerapi.StandbyChildSelectionRequest
	if !decodeJSON(w, r, &request) {
		writeProblem(w, r, 422, "invalid_selection", "Invalid Selection", "Invalid selection", 0)
		return
	}
	query := `SELECT a.id,coalesce(m.version,0) FROM tsw_target_accounts a LEFT JOIN tsw_standby_child_memberships m ON m.target_account_id=a.id WHERE `
	var arg any
	switch request.Scope {
	case ownerapi.StandbyChildSelectionRequestScopeSelected:
		if request.AccountIds == nil || request.Search != nil || request.BatchId != nil || len(*request.AccountIds) == 0 || len(*request.AccountIds) > 10000 {
			writeProblem(w, r, 422, "invalid_selection", "Invalid Selection", "Invalid selection", 0)
			return
		}
		arg = *request.AccountIds
		query += `a.id=ANY($1) `
	case ownerapi.StandbyChildSelectionRequestScopeFiltered:
		if request.AccountIds != nil || request.BatchId != nil || len(strings.TrimSpace(stringValue(request.Search))) > 254 {
			writeProblem(w, r, 422, "invalid_selection", "Invalid Selection", "Invalid selection", 0)
			return
		}
		arg = strings.ToLower(strings.TrimSpace(stringValue(request.Search)))
		query += `($1='' OR a.identifier ILIKE '%'||$1||'%' OR a.display_label ILIKE '%'||$1||'%') `
	case ownerapi.StandbyChildSelectionRequestScopeBatch:
		if request.AccountIds != nil || request.Search != nil || request.BatchId == nil {
			writeProblem(w, r, 422, "invalid_selection", "Invalid Selection", "Invalid selection", 0)
			return
		}
		arg = *request.BatchId
		query += `m.batch_id=$1 `
	default:
		writeProblem(w, r, 422, "invalid_selection", "Invalid Selection", "Invalid selection", 0)
		return
	}
	rows, err := h.pool.Query(r.Context(), query+`ORDER BY a.id LIMIT 10001`, arg)
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	defer rows.Close()
	result := ownerapi.StandbyChildSelection{Scope: ownerapi.StandbyChildSelectionScope(request.Scope), Members: []ownerapi.StandbyChildSelectionMember{}}
	for rows.Next() {
		var member ownerapi.StandbyChildSelectionMember
		if err = rows.Scan(&member.AccountId, &member.MembershipVersion); err != nil {
			h.targetFailure(w, r, err)
			return
		}
		result.Members = append(result.Members, member)
	}
	if err = rows.Err(); err != nil {
		h.targetFailure(w, r, err)
		return
	}
	if len(result.Members) > 10000 || request.Scope == ownerapi.StandbyChildSelectionRequestScopeSelected && len(result.Members) != len(*request.AccountIds) {
		writeProblem(w, r, 409, "selection_changed", "Selection Changed", "Refresh the selection", 0)
		return
	}
	result.Count = len(result.Members)
	writeJSON(w, 200, result)
}

// A 10000-member frozen selection can exceed the ordinary 512 KiB Owner JSON cap.
func decodeStandbyChange(w http.ResponseWriter, r *http.Request, value any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil {
		return false
	}
	var trailing any
	return errors.Is(decoder.Decode(&trailing), io.EOF)
}

func validStandbySelection(selection ownerapi.StandbyChildSelection, expected int) bool {
	if expected != selection.Count || expected != len(selection.Members) || expected < 0 || expected > 10000 || !selection.Scope.Valid() {
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
		if err = tx.QueryRow(r.Context(), `SELECT id FROM tsw_standby_child_batches WHERE id=$1 FOR UPDATE`, batchID).Scan(&locked); err != nil {
			standbyError(h, w, r, err)
			return
		}
	}
	name := strings.TrimSpace(request.Name)
	if !existing {
		err = tx.QueryRow(r.Context(), `INSERT INTO tsw_standby_child_batches(name) VALUES($1) RETURNING id`, name).Scan(&id)
	} else {
		var version int64
		err = tx.QueryRow(r.Context(), `SELECT version FROM tsw_standby_child_batches WHERE id=$1`, id).Scan(&version)
		if err == nil && version != *request.ExpectedVersion {
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
			_, err = tx.Exec(r.Context(), `UPDATE tsw_standby_child_batches SET version=version+1 WHERE id=$1`, previous)
			if err != nil {
				break
			}
		}
	}
	if err == nil && existing {
		var affected int64
		// A no-op same-batch addition must not advance the version.
		if changed {
			var tag pgconn.CommandTag
			tag, err = tx.Exec(r.Context(), `UPDATE tsw_standby_child_batches SET name=$1,version=version+1 WHERE id=$2 AND version=$3`, name, id, *request.ExpectedVersion)
			affected = tag.RowsAffected()
		} else {
			var tag pgconn.CommandTag
			tag, err = tx.Exec(r.Context(), `UPDATE tsw_standby_child_batches SET name=$1,version=version+1 WHERE id=$2 AND version=$3 AND name IS DISTINCT FROM $1`, name, id, *request.ExpectedVersion)
			affected = tag.RowsAffected()
			if err == nil && affected == 0 {
				var version int64
				err = tx.QueryRow(r.Context(), `SELECT version FROM tsw_standby_child_batches WHERE id=$1`, id).Scan(&version)
				if err == nil && version != *request.ExpectedVersion {
					err = errStandbyConflict
				}
			}
		}
		if err == nil && changed && affected != 1 {
			err = errStandbyConflict
		}
	}
	if err != nil {
		standbyError(h, w, r, err)
		return
	}
	result, err := standbyBatchTx(r, tx, id)
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
	err = tx.QueryRow(r.Context(), `SELECT version FROM tsw_standby_child_batches WHERE id=$1 FOR UPDATE`, id).Scan(&version)
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
