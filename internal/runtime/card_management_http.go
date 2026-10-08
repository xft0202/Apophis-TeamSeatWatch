package runtime

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	oauthdomain "github.com/xft0202/Apophis-TeamSeatWatch/internal/oauth"
	targetdomain "github.com/xft0202/Apophis-TeamSeatWatch/internal/target"
)

func (h *OwnerAuthHandler) SelectDeliveryCards(w http.ResponseWriter, r *http.Request, p ownerapi.SelectDeliveryCardsParams) {
	if _, ok := h.authenticated(w, r, false); !ok {
		return
	}
	only := true
	params := ownerapi.ListDeliveryRecordsParams{MotherAccountId: p.MotherAccountId, WorkspaceId: p.WorkspaceId, BatchId: p.BatchId, MembershipId: p.MembershipId, CardsOnly: &only, CardState: p.CardState, ServiceStatus: p.ServiceStatus, CardStatus: p.CardStatus, OrderStatus: p.OrderStatus, Search: p.Search}
	if p.MotherAccountId == nil || p.WorkspaceId == nil || !validDeliveryRecordFilters(params) || len(strings.TrimSpace(stringValue(p.Search))) > 254 {
		writeProblem(w, r, 400, "invalid_card_scope", "Invalid Request", "Choose a mother and workspace with valid filters", 0)
		return
	}
	rows, err := h.pool.Query(r.Context(), `SELECT membership.id,card.version`+deliveryRecordJoins+deliveryRecordFilterSQL+` ORDER BY card.created_at DESC,membership.id LIMIT 10001`, deliveryRecordFilterArgs(params)...)
	if err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	defer rows.Close()
	items := make([]ownerapi.CardSelectionEntry, 0)
	for rows.Next() {
		var entry ownerapi.CardSelectionEntry
		if err := rows.Scan(&entry.MembershipId, &entry.CardVersion); err != nil {
			h.deliveryFailure(w, r, err)
			return
		}
		items = append(items, entry)
	}
	if err := rows.Err(); err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	if len(items) > 10000 {
		writeProblem(w, r, 422, "card_selection_limit", "Selection Too Large", "At most 10000 cards can be selected", 0)
		return
	}
	writeJSON(w, 200, ownerapi.CardSelection{Items: items, Total: len(items)})
}

func (h *OwnerAuthHandler) openCardSecret(sealed []byte, keyVersion int16, expectedHash []byte) (string, error) {
	secret, err := targetdomain.OpenMaterial(sealed, h.keyRing)
	if err != nil {
		return "", err
	}
	hash, err := oauthdomain.LookupHMACVersion(h.keyRing, uint16(keyVersion), secret)
	if err != nil || !hmacEqual(hash[:], expectedHash) {
		return "", errors.New("stored card secret integrity check failed")
	}
	return secret, nil
}

func (h *OwnerAuthHandler) GetDeliveryCardSecret(w http.ResponseWriter, r *http.Request, id ownerapi.MembershipId, p ownerapi.GetDeliveryCardSecretParams) {
	w.Header().Set("Cache-Control", "no-store")
	if _, ok := h.authenticated(w, r, false); !ok {
		return
	}
	var sealed, hash []byte
	var version int64
	var key int16
	err := h.pool.QueryRow(r.Context(), `SELECT sealed_secret,version,hmac_key_version,lookup_hmac FROM tsw_cards WHERE membership_id=$1`, uuid.UUID(id)).Scan(&sealed, &version, &key, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "card_not_found", "Not Found", "Card was not found", 0)
		return
	}
	if err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	if version != p.CardVersion {
		writeProblem(w, r, 409, "card_version_conflict", "Conflict", "The card changed; refresh before copying", 0)
		return
	}
	if len(sealed) == 0 {
		writeProblem(w, r, 409, "card_secret_unavailable", "Secret Unavailable", "The original secret was not stored", 0)
		return
	}
	secret, err := h.openCardSecret(sealed, key, hash)
	if err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	writeJSON(w, 200, ownerapi.CardSecret{MembershipId: id, CardSecret: secret})
}

func (h *OwnerAuthHandler) ExportDeliveryCards(w http.ResponseWriter, r *http.Request, _ ownerapi.ExportDeliveryCardsParams) {
	w.Header().Set("Cache-Control", "no-store")
	if _, ok := h.authenticated(w, r, true); !ok {
		return
	}
	var request ownerapi.CardExportRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.MotherAccountId == uuid.Nil || request.WorkspaceId == uuid.Nil || len(request.Items) == 0 || len(request.Items) > 10000 {
		writeProblem(w, r, 422, "invalid_card_export", "Invalid Request", "A bounded mother/workspace card selection is required", 0)
		return
	}
	seen := make(map[uuid.UUID]bool, len(request.Items))
	for _, entry := range request.Items {
		if entry.MembershipId == uuid.Nil || entry.CardVersion < 1 || seen[entry.MembershipId] {
			writeProblem(w, r, 422, "invalid_card_export", "Invalid Request", "Card identifiers and versions must be unique and valid", 0)
			return
		}
		seen[entry.MembershipId] = true
	}
	raw, err := json.Marshal(request.Items)
	if err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	rows, err := tx.Query(r.Context(), `SELECT membership.id,target.identifier,card.sealed_secret,card.hmac_key_version,card.lookup_hmac
 FROM jsonb_to_recordset($1::jsonb) selected("membershipId" uuid,"cardVersion" bigint)
 JOIN tsw_batch_memberships membership ON membership.id=selected."membershipId"
 JOIN tsw_batches batch ON batch.id=membership.batch_id
 JOIN tsw_mother_workspace_bindings binding ON binding.id=batch.binding_id
 JOIN tsw_target_accounts target ON target.id=membership.target_account_id
 JOIN tsw_cards card ON card.membership_id=membership.id AND card.version=selected."cardVersion"
 WHERE binding.mother_account_id=$2 AND binding.workspace_id=$3 ORDER BY card.created_at DESC,membership.id
 FOR SHARE OF card,binding`, string(raw), request.MotherAccountId, request.WorkspaceId)
	if err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	defer rows.Close()
	response := ownerapi.CardExportResponse{Items: []ownerapi.CardExportItem{}, Unavailable: []uuid.UUID{}}
	count := 0
	for rows.Next() {
		var item ownerapi.CardExportItem
		var sealed, hash []byte
		var key int16
		if err := rows.Scan(&item.MembershipId, &item.TargetIdentifier, &sealed, &key, &hash); err != nil {
			h.deliveryFailure(w, r, err)
			return
		}
		count++
		if len(sealed) == 0 {
			response.Unavailable = append(response.Unavailable, item.MembershipId)
			continue
		}
		item.CardSecret, err = h.openCardSecret(sealed, key, hash)
		if err != nil {
			h.deliveryFailure(w, r, err)
			return
		}
		response.Items = append(response.Items, item)
	}
	if err := rows.Err(); err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	rows.Close()
	if count != len(request.Items) {
		writeProblem(w, r, 409, "card_selection_conflict", "Conflict", "Selected cards changed or are outside this mother/workspace", 0)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	writeJSON(w, 200, response)
}
