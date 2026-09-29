package runtime

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

type personalScope struct {
	ids    []uuid.UUID
	search string
	name   string
	label  string
}

func parsePersonalScope(ids *[]uuid.UUID, search *string) (personalScope, bool) {
	scope := personalScope{name: "filtered"}
	if ids != nil && len(*ids) > 0 {
		if len(*ids) > 10000 {
			return scope, false
		}
		seen := make(map[uuid.UUID]bool, len(*ids))
		scope.name = "selected"
		for _, id := range *ids {
			if id == uuid.Nil || seen[id] {
				return scope, false
			}
			seen[id] = true
			scope.ids = append(scope.ids, id)
		}
		// Selection takes precedence even if a filter was supplied. Hash the sorted IDs for idempotency.
		sort.Slice(scope.ids, func(i, j int) bool { return scope.ids[i].String() < scope.ids[j].String() })
		scope.label = "跨页已选账号"
	} else {
		scope.search = strings.ToLower(strings.TrimSpace(stringValue(search)))
		if utf8.RuneCountInString(scope.search) > 254 {
			return scope, false
		}
		scope.label = "全部账号"
		if scope.search != "" {
			scope.label = "全部筛选结果（当前搜索）"
		}
	}
	return scope, true
}

// A keyed fingerprint permits idempotency without retaining a searchable
// plaintext filter or an offline-guessable unkeyed digest after retention.
func personalScopeHash(scope personalScope, ring auth.KeyRing, version uint16) (string, error) {
	key, ok := ring.Lookup(version)
	if !ok {
		return "", errors.New("Personal scope key unavailable")
	}
	mac := hmac.New(sha256.New, key[:])
	mac.Write([]byte("personal-probe-scope-v1:" + scope.name + ":" + scope.search + ":" + uuidList(scope.ids)))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func uuidList(ids []uuid.UUID) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = id.String()
	}
	return strings.Join(parts, ",")
}

const personalScopeSQL = `SELECT target.id,target.identifier FROM tsw_target_accounts target WHERE
 ($1::boolean AND target.id=ANY($2::uuid[])) OR
 (NOT $1::boolean AND ($3='' OR target.identifier ILIKE '%'||$3||'%' OR target.display_label ILIKE '%'||$3||'%'))
 ORDER BY target.identifier,target.id`

func personalTargets(ctx context.Context, tx pgx.Tx, scope personalScope) ([]personalTarget, error) {
	rows, err := tx.Query(ctx, personalScopeSQL, scope.name == "selected", scope.ids, scope.search)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := []personalTarget{}
	for rows.Next() {
		var item personalTarget
		if err = rows.Scan(&item.id, &item.identifier); err != nil {
			return nil, err
		}
		targets = append(targets, item)
	}
	return targets, rows.Err()
}

type personalTarget struct {
	id         uuid.UUID
	identifier string
}

func (h *OwnerAuthHandler) PreviewPersonalProbes(w http.ResponseWriter, r *http.Request, _ ownerapi.PreviewPersonalProbesParams) {
	if _, ok := h.authenticated(w, r, true); !ok {
		return
	}
	var request ownerapi.PersonalProbeScope
	if !decodeJSON(w, r, &request) {
		writeProblem(w, r, 422, "invalid_scope", "Invalid Scope", "Invalid probe scope", 0)
		return
	}
	scope, ok := parsePersonalScope(request.TargetAccountIds, request.Search)
	if !ok {
		writeProblem(w, r, 422, "invalid_scope", "Invalid Scope", "Invalid probe scope", 0)
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	targets, err := personalTargets(r.Context(), tx, scope)
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	if scope.name == "selected" && len(targets) != len(scope.ids) {
		writeProblem(w, r, 409, "scope_changed", "Scope Changed", "Selected account no longer exists", 0)
		return
	}
	writeJSON(w, 200, ownerapi.PersonalProbePreview{Scope: ownerapi.PersonalProbePreviewScope(scope.name), Label: scope.label, Count: len(targets)})
}

func (h *OwnerAuthHandler) CreatePersonalProbes(w http.ResponseWriter, r *http.Request, _ ownerapi.CreatePersonalProbesParams) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	var request ownerapi.CreatePersonalProbesJSONRequestBody
	if !decodeJSON(w, r, &request) || !request.Confirmed || request.ExpectedCount < 1 || request.RequestKey == uuid.Nil {
		h.rejectOwnerMutation(w, r, owner, "personal_probe.create", "invalid_request", 422, "confirmation_required", "Confirmation Required", "Confirm exact scope and count")
		return
	}
	scope, ok := parsePersonalScope(request.TargetAccountIds, request.Search)
	if !ok {
		h.rejectOwnerMutation(w, r, owner, "personal_probe.create", "invalid_request", 422, "invalid_scope", "Invalid Scope", "Invalid probe scope")
		return
	}
	tx, err := h.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	// Same Owner/key is replayable only for precisely the same scope and expected count.
	if h.keyRing == nil {
		h.targetFailure(w, r, errors.New("Personal scope key unavailable"))
		return
	}
	keyVersion, _ := h.keyRing.Current()
	var existingID, existingHash string
	var existingCount int
	var existingVersion uint16
	err = tx.QueryRow(r.Context(), `SELECT id,scope_hash,total,scope_key_version FROM tsw_personal_probe_batches WHERE owner_id=$1 AND request_key=$2`, owner.OwnerID, request.RequestKey).Scan(&existingID, &existingHash, &existingCount, &existingVersion)
	if err == nil {
		fingerprint, hashErr := personalScopeHash(scope, h.keyRing, existingVersion)
		if hashErr != nil {
			h.targetFailure(w, r, hashErr)
			return
		}
		if existingHash != fingerprint || existingCount != request.ExpectedCount {
			writeProblem(w, r, 409, "request_key_conflict", "Conflict", "Request key belongs to another scope", 0)
			return
		}
		batch, readErr := h.personalBatch(r.Context(), tx, existingID)
		if readErr != nil {
			h.targetFailure(w, r, readErr)
			return
		}
		writeJSON(w, 202, batch)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		h.targetFailure(w, r, err)
		return
	}
	targets, err := personalTargets(r.Context(), tx, scope)
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	if len(targets) != request.ExpectedCount || scope.name == "selected" && len(targets) != len(scope.ids) {
		writeProblem(w, r, 409, "scope_changed", "Scope Changed", "Account scope changed; preview again", 0)
		return
	}
	fingerprint, err := personalScopeHash(scope, h.keyRing, keyVersion)
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	var id string
	err = tx.QueryRow(r.Context(), `INSERT INTO tsw_personal_probe_batches(owner_id,request_key,scope_hash,scope_key_version,scope,scope_label,total) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`, owner.OwnerID, request.RequestKey, fingerprint, keyVersion, scope.name, scope.label, len(targets)).Scan(&id)
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	for _, target := range targets {
		if _, err = tx.Exec(r.Context(), `INSERT INTO tsw_personal_probe_items(batch_id,target_account_id,identifier) VALUES ($1,$2,$3)`, id, target.id, target.identifier); err != nil {
			h.targetFailure(w, r, err)
			return
		}
	}
	batch, err := h.personalBatch(r.Context(), tx, id)
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		h.targetFailure(w, r, err)
		return
	}
	writeJSON(w, 202, batch)
}

func (h *OwnerAuthHandler) GetPersonalProbes(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	owner, ok := h.authenticated(w, r, false)
	if !ok {
		return
	}
	tx, err := h.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	batch, err := h.personalBatchForOwner(r.Context(), tx, id.String(), owner.OwnerID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "probe_not_found", "Not Found", "Probe batch not found", 0)
		return
	}
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	writeJSON(w, 200, batch)
}

func (h *OwnerAuthHandler) GetPersonalProbesByRequest(w http.ResponseWriter, r *http.Request, key uuid.UUID) {
	owner, ok := h.authenticated(w, r, false)
	if !ok {
		return
	}
	tx, err := h.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var id string
	err = tx.QueryRow(r.Context(), `SELECT id FROM tsw_personal_probe_batches WHERE owner_id=$1 AND request_key=$2`, owner.OwnerID, key).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "probe_not_found", "Not Found", "Probe request not found", 0)
		return
	}
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	batch, err := h.personalBatch(r.Context(), tx, id)
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	writeJSON(w, 200, batch)
}

func (h *OwnerAuthHandler) CancelPersonalProbes(w http.ResponseWriter, r *http.Request, id uuid.UUID, _ ownerapi.CancelPersonalProbesParams) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var found string
	err = tx.QueryRow(r.Context(), `UPDATE tsw_personal_probe_batches SET canceled_at=COALESCE(canceled_at,now()) WHERE id=$1 AND owner_id=$2 RETURNING id`, id, owner.OwnerID).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "probe_not_found", "Not Found", "Probe batch not found", 0)
		return
	}
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE tsw_personal_probe_items SET status='canceled',finished_at=now() WHERE batch_id=$1 AND status IN ('queued','running')`, id)
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	batch, err := h.personalBatch(r.Context(), tx, found)
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		h.targetFailure(w, r, err)
		return
	}
	writeJSON(w, 200, batch)
}

func (h *OwnerAuthHandler) personalBatchForOwner(ctx context.Context, tx pgx.Tx, id, ownerID string) (ownerapi.PersonalProbeBatch, error) {
	var match string
	err := tx.QueryRow(ctx, `SELECT id FROM tsw_personal_probe_batches WHERE id=$1 AND owner_id=$2`, id, ownerID).Scan(&match)
	if err != nil {
		return ownerapi.PersonalProbeBatch{}, err
	}
	return h.personalBatch(ctx, tx, match)
}
func (h *OwnerAuthHandler) personalBatch(ctx context.Context, tx pgx.Tx, id string) (ownerapi.PersonalProbeBatch, error) {
	batch := ownerapi.PersonalProbeBatch{Items: []ownerapi.PersonalProbeItem{}}
	err := tx.QueryRow(ctx, `SELECT id,scope,scope_label,total,created_at,canceled_at FROM tsw_personal_probe_batches WHERE id=$1`, id).Scan(&batch.Id, &batch.Scope, &batch.Label, &batch.Total, &batch.CreatedAt, &batch.CanceledAt)
	if err != nil {
		return batch, err
	}
	rows, err := tx.Query(ctx, `SELECT target_account_id,identifier,status,outcome,endpoint,http_status,evidence_code,verified_evidence,attempt_count,started_at,finished_at FROM tsw_personal_probe_items WHERE batch_id=$1 ORDER BY identifier,target_account_id`, id)
	if err != nil {
		return batch, err
	}
	defer rows.Close()
	for rows.Next() {
		var item ownerapi.PersonalProbeItem
		if err = rows.Scan(&item.TargetAccountId, &item.Identifier, &item.Status, &item.Outcome, &item.Endpoint, &item.HttpStatus, &item.EvidenceCode, &item.VerifiedEvidence, &item.AttemptCount, &item.StartedAt, &item.FinishedAt); err != nil {
			return batch, err
		}
		switch item.Status {
		case "queued":
			batch.Queued++
		case "running":
			batch.Running++
		case "succeeded":
			batch.Succeeded++
		case "failed":
			batch.Failed++
		case "canceled":
			batch.Canceled++
		}
		batch.Items = append(batch.Items, item)
	}
	batch.NotSavedOrRetained = batch.Total - len(batch.Items)
	return batch, rows.Err()
}
