package runtime

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

func (h *OwnerAuthHandler) GetMotherDiscovery(w http.ResponseWriter, r *http.Request, accountID openapi_types.UUID) {
	if _, ok := h.authenticated(w, r, false); !ok {
		return
	}
	result, err := h.motherDiscovery(r, accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "mother_not_found", "Not Found", "Mother account was not found", 0)
		return
	}
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *OwnerAuthHandler) RunMotherDiscovery(w http.ResponseWriter, r *http.Request, accountID openapi_types.UUID, _ ownerapi.RunMotherDiscoveryParams) {
	if _, ok := h.authenticated(w, r, true); !ok {
		return
	}
	// Serialize attempts and material edits on the account row. Production has no
	// remote adapter and never sends credentials or initiates network activity.
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var material platform.MotherMaterial
	var password, totp []byte
	var revision int64
	var active bool
	err = tx.QueryRow(r.Context(), `SELECT credential.login_identifier,credential.password_secret,credential.totp_secret,credential.secret_revision,account.status='active' FROM tsw_mother_accounts account JOIN tsw_mother_account_credentials credential ON credential.mother_account_id=account.id WHERE account.id=$1 FOR UPDATE OF account,credential`, accountID).Scan(&material.LoginIdentifier, &password, &totp, &revision, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "mother_not_found", "Not Found", "Mother account was not found", 0)
		return
	}
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	if !active {
		writeProblem(w, r, http.StatusConflict, "mother_disabled", "Account Disabled", "Mother account is disabled", 0)
		return
	}
	material.Password, material.TOTPSecret = string(password), string(totp)
	status := "missing_credentials"
	var found []platform.DiscoveredWorkspace
	_, totpErr := auth.TOTPCode(material.TOTPSecret, time.Now())
	if strings.TrimSpace(material.LoginIdentifier) != "" && material.Password != "" && totpErr == nil {
		adapter := h.discovery
		if adapter == nil {
			adapter = platform.UnavailableDiscovery{}
		}
		var result platform.DiscoveryResult
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		result, err = adapter.VerifyAndDiscover(ctx, material)
		cancel()
		switch {
		case errors.Is(err, platform.ErrDiscoveryUnavailable):
			status = "unavailable"
		case err != nil:
			status = "discovery_failed"
		case !platform.ValidateDiscovery(result):
			status = "discovery_failed"
		default:
			status = result.Status
			if status == "discovered" {
				found = result.Workspaces
				if len(found) == 0 {
					status = "empty"
				}
			}
		}
	}
	runID := uuid.New()
	_, err = tx.Exec(r.Context(), `INSERT INTO tsw_mother_discoveries(mother_account_id,run_id,secret_revision,status) VALUES ($1,$2,$3,$4) ON CONFLICT (mother_account_id) DO UPDATE SET run_id=EXCLUDED.run_id,secret_revision=EXCLUDED.secret_revision,status=EXCLUDED.status,observed_at=now()`, accountID, runID, revision, status)
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM tsw_mother_workspace_visibility WHERE mother_account_id=$1`, accountID)
	}
	for _, item := range found {
		if err != nil {
			break
		}
		var workspaceID uuid.UUID
		// Canonical identity is the platform workspace ID; names never identify workspaces.
		err = tx.QueryRow(r.Context(), `INSERT INTO tsw_workspaces(platform_workspace_id,display_name) VALUES ($1,$2) ON CONFLICT (platform_workspace_id) DO UPDATE SET platform_workspace_id=EXCLUDED.platform_workspace_id RETURNING id`, item.PlatformID, item.Name).Scan(&workspaceID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO tsw_workspace_projections(workspace_id) VALUES ($1) ON CONFLICT (workspace_id) DO NOTHING`, workspaceID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO tsw_mother_workspace_visibility(mother_account_id,workspace_id,run_id,access_status) VALUES ($1,$2,$3,$4)`, accountID, workspaceID, runID, item.Access)
		}
	}
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	response, err := h.motherDiscovery(r, accountID)
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *OwnerAuthHandler) motherDiscovery(r *http.Request, accountID uuid.UUID) (ownerapi.MotherDiscovery, error) {
	response := ownerapi.MotherDiscovery{MotherAccountId: accountID, Status: ownerapi.MotherDiscoveryStatusNotVerified, Workspaces: []ownerapi.MotherVisibleWorkspace{}}
	var status *string
	var observed *time.Time
	err := h.pool.QueryRow(r.Context(), `SELECT discovery.status,discovery.observed_at FROM tsw_mother_accounts account JOIN tsw_mother_account_credentials credential ON credential.mother_account_id=account.id LEFT JOIN tsw_mother_discoveries discovery ON discovery.mother_account_id=account.id AND discovery.secret_revision=credential.secret_revision AND discovery.observed_at>now()-interval '7 days' WHERE account.id=$1 AND account.status='active'`, accountID).Scan(&status, &observed)
	if err != nil {
		return response, err
	}
	if status == nil {
		return response, nil
	}
	response.Status, response.ObservedAt = ownerapi.MotherDiscoveryStatus(*status), observed
	if *status != "discovered" {
		return response, nil
	}
	rows, err := h.pool.Query(r.Context(), `SELECT workspace.id,workspace.display_name,visibility.access_status FROM tsw_mother_workspace_visibility visibility JOIN tsw_workspaces workspace ON workspace.id=visibility.workspace_id JOIN tsw_mother_discoveries discovery ON discovery.mother_account_id=visibility.mother_account_id AND discovery.run_id=visibility.run_id AND discovery.observed_at>now()-interval '7 days' JOIN tsw_mother_account_credentials credential ON credential.mother_account_id=visibility.mother_account_id AND credential.secret_revision=discovery.secret_revision WHERE visibility.mother_account_id=$1 ORDER BY workspace.display_name,workspace.id`, accountID)
	if err != nil {
		return response, err
	}
	defer rows.Close()
	for rows.Next() {
		var item ownerapi.MotherVisibleWorkspace
		if err := rows.Scan(&item.Id, &item.DisplayName, &item.AccessStatus); err != nil {
			return response, err
		}
		response.Workspaces = append(response.Workspaces, item)
	}
	return response, rows.Err()
}
