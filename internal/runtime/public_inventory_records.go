package runtime

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

const publicInventoryRecordFrom = ` FROM public.tsw_batch_zip_archives archive
	JOIN public.tsw_standby_child_batches batch ON batch.id=archive.batch_id
	JOIN public.tsw_workspaces workspace ON workspace.id=archive.workspace_id
	LEFT JOIN public.tsw_public_zip_inventory inventory ON inventory.package_id=archive.id AND inventory.owner_id=archive.owner_id
	LEFT JOIN public.tsw_public_zip_orders ord ON ord.inventory_id=inventory.id AND ord.package_id=archive.id
	WHERE archive.owner_id=$1
	AND ($2='' OR strpos(lower(batch.name),lower($2))>0 OR strpos(lower(workspace.display_name),lower($2))>0 OR strpos(lower(COALESCE(inventory.display_suffix,'')),lower($2))>0)
	AND ($3='' OR ($3='claimed' AND ord.id IS NOT NULL) OR ($3='unclaimed' AND ord.id IS NULL))`

// ListPublicZIPInventory reads the same immutable package and order objects used
// by Public. Legacy single-account delivery records are a separate contract.
func (h *OwnerAuthHandler) ListPublicZIPInventory(w http.ResponseWriter, r *http.Request, params ownerapi.ListPublicZIPInventoryParams) {
	owner, ok := h.authenticated(w, r, false)
	if !ok {
		return
	}
	page, size, ok := pagination(params.Page, params.PageSize)
	search, orderStatus := "", ""
	if params.Search != nil {
		search = strings.TrimSpace(*params.Search)
	}
	if params.OrderStatus != nil {
		orderStatus = string(*params.OrderStatus)
	}
	if !ok || utf8.RuneCountInString(search) > 200 || (orderStatus != "" && orderStatus != "claimed" && orderStatus != "unclaimed") {
		writeProblem(w, r, 400, "invalid_delivery_filter", "Invalid Request", "The record filters are invalid", 0)
		return
	}
	response := ownerapi.PublicZIPInventoryRecordList{Items: []ownerapi.PublicZIPInventoryRecord{}, Page: page, PageSize: size}
	if err := h.pool.QueryRow(r.Context(), `SELECT count(*)`+publicInventoryRecordFrom, owner.OwnerID, search, orderStatus).Scan(&response.Total); err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	rows, err := h.pool.Query(r.Context(), `SELECT archive.id,archive.preview_id,archive.batch_id,batch.name,
		archive.workspace_id,workspace.display_name,archive.account_count,
		CASE WHEN inventory.id IS NULL THEN 'not_activated' WHEN inventory.revoked_at IS NOT NULL THEN 'revoked'
		WHEN NOT inventory.enabled OR inventory.access_expires_at<=clock_timestamp() THEN 'unavailable' ELSE 'active' END,
		inventory.display_suffix,ord.id IS NOT NULL,ord.id,archive.created_at,inventory.created_at,ord.created_at,
		inventory.claim_expires_at,inventory.access_expires_at`+publicInventoryRecordFrom+`
		ORDER BY archive.created_at DESC,archive.id DESC LIMIT $4 OFFSET $5`, owner.OwnerID, search, orderStatus, size, (page-1)*size)
	if err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var item ownerapi.PublicZIPInventoryRecord
		if err := rows.Scan(&item.PackageId, &item.PreviewId, &item.BatchId, &item.BatchName, &item.WorkspaceId,
			&item.WorkspaceName, &item.AccountCount, &item.CardStatus, &item.CardSuffix, &item.HasOrder,
			&item.OrderId, &item.CreatedAt, &item.GeneratedAt, &item.RedeemedAt, &item.ClaimExpiresAt, &item.AccessExpiresAt); err != nil {
			h.deliveryFailure(w, r, err)
			return
		}
		response.Items = append(response.Items, item)
	}
	if err := rows.Err(); err != nil {
		h.deliveryFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}
