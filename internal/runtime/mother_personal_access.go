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

func (h *OwnerAuthHandler) GetMotherPersonalAccess(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	if _, ok := h.authenticated(w, r, false); !ok {
		return
	}
	result, err := h.motherPersonalAccess(r, id)
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

func (h *OwnerAuthHandler) RefreshMotherPersonalAccess(w http.ResponseWriter, r *http.Request, id openapi_types.UUID, _ ownerapi.RefreshMotherPersonalAccessParams) {
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
	var material platform.MotherMaterial
	var password, totp []byte
	var revision int64
	var active bool
	err = tx.QueryRow(r.Context(), `SELECT credential.login_identifier,credential.password_secret,credential.totp_secret,credential.secret_revision,account.status='active' FROM tsw_mother_accounts account JOIN tsw_mother_account_credentials credential ON credential.mother_account_id=account.id WHERE account.id=$1 FOR UPDATE OF account,credential`, id).Scan(&material.LoginIdentifier, &password, &totp, &revision, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "mother_not_found", "Not Found", "Mother account was not found", 0)
		return
	}
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	if !active {
		writeProblem(w, r, 409, "mother_disabled", "Account Disabled", "Mother account is disabled", 0)
		return
	}
	material.Password, material.TOTPSecret = string(password), string(totp)
	status := "missing_credentials"
	var session platform.PersonalSession
	_, totpErr := auth.TOTPCode(material.TOTPSecret, time.Now())
	if strings.TrimSpace(material.LoginIdentifier) != "" && material.Password != "" && totpErr == nil {
		adapter := h.personalRefresh
		if adapter == nil {
			adapter = platform.UnavailablePersonalRefresh{}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		result, refreshErr := adapter.RefreshPersonal(ctx, material)
		cancel()
		switch {
		case errors.Is(refreshErr, platform.ErrPersonalRefreshUnavailable):
			status = "unavailable"
		case refreshErr != nil:
			status = "refresh_failed"
		case !platform.ValidatePersonalRefresh(result, time.Now()):
			status = "refresh_failed"
		default:
			status = result.Status
			session = result.Session
		}
	}
	// A failed refresh cannot leave a prior session or visibility silently usable.
	_, err = tx.Exec(r.Context(), `DELETE FROM tsw_mother_personal_sessions WHERE mother_account_id=$1`, id)
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM tsw_mother_workspace_visibility WHERE mother_account_id=$1`, id)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM tsw_mother_discoveries WHERE mother_account_id=$1`, id)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO tsw_mother_personal_access(mother_account_id,secret_revision,status) VALUES ($1,$2,$3) ON CONFLICT(mother_account_id) DO UPDATE SET secret_revision=EXCLUDED.secret_revision,status=EXCLUDED.status,checked_at=now()`, id, revision, status)
	}
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	if status == "ready" {
		version, nonce, sealed, sealErr := sealPersonalSession(h.keyRing, id, revision, session)
		if sealErr != nil {
			h.workspaceFailure(w, r, sealErr)
			return
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO tsw_mother_personal_sessions(mother_account_id,secret_revision,generation,key_version,nonce,sealed_session,expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`, id, revision, uuid.New(), version, nonce, sealed, session.ExpiresAt)
		if err != nil {
			h.workspaceFailure(w, r, err)
			return
		}
	}
	// Persisted platform session material is sensitive; replace Owner token atomically.
	var token string
	var idle time.Time
	if status == "ready" {
		token, idle, err = h.rotateSessionTx(r.Context(), tx, owner, "mother_personal_refresh", r)
	}
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	if token != "" {
		auth.SetSessionCookie(w, token, idle, h.secureCookies)
	}
	response, err := h.motherPersonalAccess(r, id)
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *OwnerAuthHandler) motherPersonalAccess(r *http.Request, id uuid.UUID) (ownerapi.MotherPersonalAccess, error) {
	response := ownerapi.MotherPersonalAccess{MotherAccountId: id, Status: ownerapi.MotherPersonalAccessStatusNotVerified}
	var status *string
	var checked *time.Time
	err := h.pool.QueryRow(r.Context(), `SELECT CASE WHEN access.status='ready' AND (session.mother_account_id IS NULL OR session.expires_at<=now()) THEN NULL ELSE access.status END,access.checked_at FROM tsw_mother_accounts account JOIN tsw_mother_account_credentials credential ON credential.mother_account_id=account.id LEFT JOIN tsw_mother_personal_access access ON access.mother_account_id=account.id AND access.secret_revision=credential.secret_revision LEFT JOIN tsw_mother_personal_sessions session ON session.mother_account_id=account.id AND session.secret_revision=credential.secret_revision WHERE account.id=$1 AND account.status='active'`, id).Scan(&status, &checked)
	if err != nil {
		return response, err
	}
	if status != nil {
		response.Status = ownerapi.MotherPersonalAccessStatus(*status)
		response.CheckedAt = checked
	}
	return response, nil
}
