package runtime

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
	targetdomain "github.com/xft0202/Apophis-TeamSeatWatch/internal/target"
)

type targetRefreshAttempt struct {
	material          platform.MotherMaterial
	revision, attempt int64
	active            bool
	complete          bool
}

func (h *OwnerAuthHandler) reserveTargetRefresh(ctx context.Context, id uuid.UUID) (targetRefreshAttempt, error) {
	var reservation targetRefreshAttempt
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return reservation, err
	}
	defer tx.Rollback(ctx)
	var password, totp []byte
	err = tx.QueryRow(ctx, `SELECT target.identifier,credential.password_secret,credential.totp_secret,credential.secret_revision,target.status='active',credential.material_status='complete' AND credential.materials_sealed FROM tsw_target_accounts target JOIN tsw_target_credentials credential ON credential.target_account_id=target.id WHERE target.id=$1 FOR UPDATE OF target,credential`, id).Scan(&reservation.material.LoginIdentifier, &password, &totp, &reservation.revision, &reservation.active, &reservation.complete)
	if err != nil || !reservation.active {
		return reservation, err
	}
	if reservation.complete {
		reservation.material.Password, err = targetdomain.OpenMaterial(password, h.keyRing)
		if err != nil {
			return reservation, err
		}
		reservation.material.TOTPSecret, err = targetdomain.OpenMaterial(totp, h.keyRing)
		if err != nil {
			return reservation, err
		}
		reservation.complete = reservation.material.Password != "" && targetdomain.CompleteTOTP(reservation.material.TOTPSecret)
	}
	err = tx.QueryRow(ctx, `INSERT INTO tsw_target_personal_access(target_account_id,secret_revision,status) VALUES ($1,$2,'verifying') ON CONFLICT(target_account_id) DO UPDATE SET secret_revision=EXCLUDED.secret_revision,attempt=tsw_target_personal_access.attempt+1,status='verifying',checked_at=now() RETURNING attempt`, id, reservation.revision).Scan(&reservation.attempt)
	if err == nil {
		_, err = tx.Exec(ctx, `DELETE FROM tsw_target_personal_sessions WHERE target_account_id=$1`, id)
	}
	if err != nil {
		return reservation, err
	}
	return reservation, tx.Commit(ctx)
}

func (h *OwnerAuthHandler) GetTargetPersonalAccess(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	if _, ok := h.authenticated(w, r, false); !ok {
		return
	}
	response, err := h.targetPersonalAccess(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "target_not_found", "Not Found", "Target account was not found", 0)
		return
	}
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *OwnerAuthHandler) RefreshTargetPersonalAccess(w http.ResponseWriter, r *http.Request, id openapi_types.UUID, _ ownerapi.RefreshTargetPersonalAccessParams) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	reserved, err := h.reserveTargetRefresh(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "target_not_found", "Not Found", "Target account was not found", 0)
		return
	}
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	if !reserved.active {
		writeProblem(w, r, 409, "target_disabled", "Account Disabled", "Target account is disabled", 0)
		return
	}
	status := "missing_credentials"
	var session platform.PersonalSession
	if reserved.complete {
		adapter := h.personalRefresh
		if adapter == nil {
			adapter = platform.UnavailablePersonalRefresh{}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		result, refreshErr := adapter.RefreshPersonal(ctx, reserved.material)
		cancel()
		switch {
		case errors.Is(refreshErr, platform.ErrPersonalRefreshUnavailable):
			status = "unavailable"
		case refreshErr != nil || !platform.ValidatePersonalRefresh(result, time.Now()):
			status = "refresh_failed"
		default:
			status, session = result.Status, result.Session
		}
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var revision, attempt int64
	var active bool
	err = tx.QueryRow(r.Context(), `SELECT credential.secret_revision,access.attempt,target.status='active' FROM tsw_target_accounts target JOIN tsw_target_credentials credential ON credential.target_account_id=target.id JOIN tsw_target_personal_access access ON access.target_account_id=target.id WHERE target.id=$1 FOR UPDATE OF target,credential,access`, id).Scan(&revision, &attempt, &active)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!active || revision != reserved.revision || attempt != reserved.attempt)) {
		writeProblem(w, r, 409, "target_access_changed", "Conflict", "Target access changed; refresh again", 0)
		return
	}
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	// Preserve the existing target/credential row order, but take Owner before
	// any Personal write trigger takes candidate's action gate. Taking candidate
	// before Owner would invert the dispatcher's ordered action gates; taking an
	// action gate before these rows would invert existing row-first writers.
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('tsw.rotation.action.owner/'||$1::text,0))`, owner.OwnerID); err != nil {
		h.targetFailure(w, r, err)
		return
	}
	if status == "ready" {
		version, nonce, sealed, sealErr := sealSessionFor("target", h.keyRing, id, revision, session)
		if sealErr != nil {
			h.targetFailure(w, r, sealErr)
			return
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO tsw_target_personal_sessions(target_account_id,secret_revision,attempt,generation,key_version,nonce,sealed_session,expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, id, revision, attempt, uuid.New(), version, nonce, sealed, session.ExpiresAt)
		if err != nil {
			h.targetFailure(w, r, err)
			return
		}
	}
	_, err = tx.Exec(r.Context(), `UPDATE tsw_target_personal_access SET status=$2,checked_at=now() WHERE target_account_id=$1`, id, status)
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	var token string
	var idle time.Time
	if status == "ready" {
		token, idle, err = h.rotateSessionTx(r.Context(), tx, owner, "session_revocation", r)
	}
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		h.targetFailure(w, r, err)
		return
	}
	if token != "" {
		auth.SetSessionCookie(w, token, idle, h.secureCookies)
	}
	response, err := h.targetPersonalAccess(r.Context(), id)
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *OwnerAuthHandler) targetPersonalAccess(ctx context.Context, id uuid.UUID) (ownerapi.TargetPersonalAccess, error) {
	response := ownerapi.TargetPersonalAccess{TargetAccountId: id, Status: ownerapi.TargetPersonalAccessStatusNotVerified}
	var status *string
	var checked, expires *time.Time
	var revision int64
	var version *int16
	var nonce, sealed []byte
	err := h.pool.QueryRow(ctx, `SELECT CASE WHEN access.status='ready' AND (session.target_account_id IS NULL OR session.expires_at<=now()) THEN 'session_expired' WHEN access.status='verifying' AND access.checked_at<now()-interval '1 minute' THEN 'refresh_failed' ELSE access.status END,access.checked_at,session.expires_at,credential.secret_revision,session.key_version,session.nonce,session.sealed_session FROM tsw_target_accounts target JOIN tsw_target_credentials credential ON credential.target_account_id=target.id LEFT JOIN tsw_target_personal_access access ON access.target_account_id=target.id AND access.secret_revision=credential.secret_revision LEFT JOIN tsw_target_personal_sessions session ON session.target_account_id=target.id AND session.secret_revision=credential.secret_revision AND session.attempt=access.attempt WHERE target.id=$1 AND target.status='active'`, id).Scan(&status, &checked, &expires, &revision, &version, &nonce, &sealed)
	if err != nil {
		return response, err
	}
	if status != nil {
		if *status == "ready" {
			if version == nil || expires == nil {
				*status = "refresh_failed"
			} else {
				session, openErr := openSessionFor("target", h.keyRing, id, revision, uint16(*version), nonce, sealed)
				if openErr != nil || !session.ExpiresAt.UTC().Truncate(time.Microsecond).Equal(expires.UTC()) || !platform.ValidatePersonalRefresh(platform.PersonalRefreshResult{Status: "ready", Session: session}, time.Now()) {
					*status = "refresh_failed"
				}
			}
		}
		response.Status = ownerapi.TargetPersonalAccessStatus(*status)
		response.CheckedAt = checked
		if *status == "ready" {
			response.ExpiresAt = expires
		}
	}
	return response, nil
}
