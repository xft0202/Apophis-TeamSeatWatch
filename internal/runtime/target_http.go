package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/audit"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/identity"
	targetdomain "github.com/xft0202/Apophis-TeamSeatWatch/internal/target"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/task"
)

func targetMaterialStatus(totp string) string {
	if targetdomain.CompleteTOTP(totp) {
		return "complete"
	}
	return "needs_totp"
}

func (h *OwnerAuthHandler) sealTargetFields(password, totp, recovery string) ([]byte, []byte, []byte, string, error) {
	sealedPassword, err := targetdomain.SealMaterial(password, h.keyRing)
	if err != nil {
		return nil, nil, nil, "", err
	}
	sealedTotp, err := targetdomain.SealMaterial(totp, h.keyRing)
	if err != nil {
		return nil, nil, nil, "", err
	}
	sealedRecovery, err := targetdomain.SealMaterial(recovery, h.keyRing)
	return sealedPassword, sealedTotp, sealedRecovery, targetMaterialStatus(totp), err
}

func scanTargetAccount(scanner interface{ Scan(...any) error }) (ownerapi.TargetAccount, error) {
	var item ownerapi.TargetAccount
	var probeStatus *string
	err := scanner.Scan(
		&item.Id, &item.Identifier, &item.DisplayLabel, &item.Status,
		&item.HasPassword, &item.HasTotp, &item.HasRecovery, &item.MaterialStatus, &item.SecretRevision,
		&probeStatus, &item.LatestProbeHttpStatus, &item.LatestProbeErrorCode,
		&item.LatestProbeEndpoint, &item.LatestProbeOrigin, &item.LatestProbedAt,
		&item.LastVerifiedAt, &item.Version, &item.UpdatedAt,
	)
	if err == nil && probeStatus != nil {
		status := ownerapi.TargetProbeClassification(*probeStatus)
		item.LatestProbeStatus = &status
	}
	return item, err
}

const targetProjectionSQL = `target.id,target.identifier,target.display_label,target.status,
	octet_length(credentials.password_secret)>0,credentials.material_status='complete',
	credentials.recovery_secret IS NOT NULL,credentials.material_status,credentials.secret_revision,
	credentials.latest_probe_status,credentials.latest_probe_http_status,
	credentials.latest_probe_error_code,credentials.latest_probe_endpoint_key,
	credentials.latest_probe_origin,credentials.latest_probed_at,
	credentials.last_verified_at,target.version,target.updated_at`

// Personal classifications are resolved on the server so result filters and
// pagination describe the same saved facts without loading a whole probe batch.
const targetListPersonalSQL = ` LEFT JOIN LATERAL (
 SELECT CASE item.outcome WHEN 'available' THEN 'available' WHEN 'credential_invalid' THEN 'credential_invalid'
 WHEN 'forbidden' THEN 'account_problem' WHEN 'banned' THEN 'definitely_unavailable'
 WHEN 'network_error' THEN 'transient_failure' ELSE 'unknown' END AS classification,
 item.http_status,item.evidence_code,item.endpoint,item.finished_at
 FROM tsw_personal_probe_items item JOIN tsw_personal_probe_batches batch ON batch.id=item.batch_id
 WHERE item.target_account_id=target.id AND batch.owner_id=$6 AND item.outcome IS NOT NULL
 AND item.finished_at>=target.updated_at
 AND item.finished_at>=COALESCE(credentials.latest_probed_at,target.updated_at)
 ORDER BY item.finished_at DESC,item.batch_id DESC LIMIT 1
 ) personal ON true `

var targetListProjectionSQL = strings.NewReplacer(
	"credentials.latest_probe_status", "COALESCE(personal.classification,credentials.latest_probe_status)",
	"credentials.latest_probe_http_status", "CASE WHEN personal.finished_at IS NOT NULL THEN personal.http_status ELSE credentials.latest_probe_http_status END",
	"credentials.latest_probe_error_code", "CASE WHEN personal.finished_at IS NOT NULL THEN personal.evidence_code ELSE credentials.latest_probe_error_code END",
	"credentials.latest_probe_endpoint_key", "CASE WHEN personal.finished_at IS NOT NULL THEN personal.endpoint ELSE credentials.latest_probe_endpoint_key END",
	"credentials.latest_probe_origin", "CASE WHEN personal.finished_at IS NOT NULL THEN 'personal' ELSE credentials.latest_probe_origin END",
	"credentials.latest_probed_at", "COALESCE(personal.finished_at,credentials.latest_probed_at)",
).Replace(targetProjectionSQL)

func (h *OwnerAuthHandler) listTargetAccounts(w http.ResponseWriter, r *http.Request, params ownerapi.ListTargetAccountsParams) {
	owner, authenticated := h.authenticated(w, r, false)
	if !authenticated {
		return
	}
	page, size, ok := pagination(params.Page, params.PageSize)
	if !ok {
		writeProblem(w, r, 400, "invalid_pagination", "Invalid Request", "Pagination is invalid", 0)
		return
	}
	sortValue, statusValue, probeValue, searchValue, domainValue, tokenValue := "", "", "", "", "", ""
	if params.Sort != nil {
		sortValue = string(*params.Sort)
	}
	if params.Status != nil {
		statusValue = string(*params.Status)
	}
	if params.ProbeStatus != nil {
		probeValue = string(*params.ProbeStatus)
	}
	if params.Search != nil {
		searchValue = strings.ToLower(strings.TrimSpace(*params.Search))
	}
	if params.Domain != nil {
		domainValue = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(*params.Domain)), "@")
	}
	if params.TokenStatus != nil {
		tokenValue = string(*params.TokenStatus)
	}
	order := "target.created_at DESC,target.id DESC"
	switch sortValue {
	case "", "created_desc":
	case "identifier_asc":
		order = "target.identifier ASC"
	case "probed_desc":
		order = "COALESCE(personal.finished_at,credentials.latest_probed_at) DESC NULLS LAST,target.created_at DESC,target.id DESC"
	default:
		writeProblem(w, r, 400, "invalid_sort", "Invalid Request", "Sort is not allowed", 0)
		return
	}
	if statusValue != "" && statusValue != "active" && statusValue != "disabled" ||
		!validTargetProbeFilter(probeValue) || !validTargetTokenFilter(tokenValue) || utf8.RuneCountInString(searchValue) > 254 || utf8.RuneCountInString(domainValue) > 254 || strings.ContainsAny(domainValue, " @\t\r\n") {
		writeProblem(w, r, 400, "invalid_filter", "Invalid Request", "Filter is not allowed", 0)
		return
	}
	membershipValue := ""
	if params.MembershipStatus != nil {
		membershipValue = string(*params.MembershipStatus)
	}
	if !validStandbyMembershipFilter(membershipValue) {
		writeProblem(w, r, 400, "invalid_filter", "Invalid Request", "Invalid membership filter", 0)
		return
	}
	from := ` FROM tsw_target_accounts target JOIN tsw_target_credentials credentials ON credentials.target_account_id=target.id
 LEFT JOIN tsw_standby_child_memberships standby_membership ON standby_membership.target_account_id=target.id
 LEFT JOIN tsw_standby_child_batches standby_batch ON standby_batch.id=standby_membership.batch_id `
	where := ` WHERE ($1='' OR target.status=$1)
 AND ($3='' OR target.identifier ILIKE '%'||$3||'%' OR target.display_label ILIKE '%'||$3||'%')
 AND ($4='' OR split_part(target.identifier,'@',2)=$4)
 AND ($7='' OR ($7='unassigned' AND standby_membership.batch_id IS NULL) OR ($7='assigned' AND standby_membership.batch_id IS NOT NULL))
 AND ($8::uuid IS NULL OR standby_membership.batch_id=$8)
 AND ($9::uuid IS NULL OR standby_membership.batch_id IS DISTINCT FROM $9) `
	probeWhere := ` AND ($2='' OR ($2='unprobed' AND COALESCE(personal.classification,credentials.latest_probe_status) IS NULL)
 OR COALESCE(personal.classification,credentials.latest_probe_status)=$2) `
	filteredFrom, tokenWhere := from, ` AND $5::text='' `
	if tokenValue != "" {
		filteredFrom += targetListTokensSQL
		tokenWhere = targetTokenFilterSQL
	}
	args := []any{statusValue, probeValue, searchValue, domainValue, tokenValue, owner.OwnerID, membershipValue, params.StandbyBatchId, params.ExcludeStandbyBatchId}
	countQuery, countArgs := `SELECT count(*)`+filteredFrom+where+tokenWhere+` AND $2::text='' AND $6::uuid IS NOT NULL`, args
	if probeValue != "" {
		countQuery, countArgs = `SELECT count(*)`+filteredFrom+targetListPersonalSQL+where+probeWhere+tokenWhere, args
	}
	response := ownerapi.TargetAccountList{Items: []ownerapi.TargetAccount{}, Page: page, PageSize: size}
	if err := h.pool.QueryRow(r.Context(), countQuery, countArgs...).Scan(&response.Total); err != nil {
		h.targetFailure(w, r, err)
		return
	}
	projection := targetListProjectionSQL + `,tokens.has_at,tokens.has_rt,standby_batch.id,standby_batch.name`
	query := `SELECT ` + projection + from + targetListPersonalSQL + targetListTokensSQL + where + probeWhere + tokenWhere +
		` ORDER BY ` + order + ` LIMIT $10 OFFSET $11`
	if probeValue == "" && sortValue != "probed_desc" {
		// Resolve the page before looking up Personal history. Ordinary browsing
		// performs at most page_size history lookups, regardless of total accounts.
		query = `WITH page_targets AS MATERIALIZED (SELECT target.id` + filteredFrom + where + tokenWhere + ` AND $2::text='' ORDER BY ` + order + ` LIMIT $10 OFFSET $11)
 SELECT ` + projection + from + ` JOIN page_targets ON page_targets.id=target.id ` + targetListPersonalSQL + targetListTokensSQL + ` ORDER BY ` + order
	}
	rows, err := h.pool.Query(r.Context(), query, append(args, size, (page-1)*size)...)
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		tokens := ownerapi.TargetAccountTokenStatus{}
		var batchID *uuid.UUID
		var batchName *string
		item, err := scanTargetAccount(rowWithStandbyStatus{rowWithTokenStatus: rowWithTokenStatus{scanner: rows, tokens: &tokens}, batchID: &batchID, batchName: &batchName})
		if err != nil {
			h.targetFailure(w, r, err)
			return
		}
		item.TokenStatus = &tokens
		if batchID != nil && batchName != nil {
			item.StandbyBatch = &ownerapi.StandbyChildBatchRef{Id: *batchID, Name: *batchName}
		}
		response.Items = append(response.Items, item)
	}
	if err := rows.Err(); err != nil {
		h.targetFailure(w, r, err)
		return
	}
	rows.Close()
	if err := h.loadPagePersonalAccess(r.Context(), response.Items); err != nil {
		h.targetFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

type rowWithTotal struct {
	pgx.Rows
	total *int64
}

func (row rowWithTotal) Scan(values ...any) error {
	return row.Rows.Scan(append(values, row.total)...)
}

func validTargetProbeFilter(value string) bool {
	switch value {
	case "", "available", "credential_invalid", "account_problem", "definitely_unavailable", "transient_failure", "unknown", "unprobed":
		return true
	default:
		return false
	}
}

func (h *OwnerAuthHandler) createTargetAccount(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	var request ownerapi.CreateTargetAccountJSONRequestBody
	if !decodeJSON(w, r, &request) {
		h.rejectOwnerMutation(w, r, owner, "target_account.create", "invalid_request", 422, "invalid_target_account", "Invalid Target Account", "Target account fields are invalid")
		return
	}
	identifier, display := strings.ToLower(strings.TrimSpace(request.Identifier)), strings.TrimSpace(stringValue(request.DisplayLabel))
	if display == "" {
		display = identifier
	}
	if !validTargetInput(identifier, display, request.Password, stringValue(request.TotpSecret), stringValue(request.RecoverySecret), stringValue(request.PlatformSubjectId)) {
		h.rejectOwnerMutation(w, r, owner, "target_account.create", "invalid_request", 422, "invalid_target_account", "Invalid Target Account", "Target account fields are invalid")
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	item, err := h.insertTargetAccountTx(r, tx, identifier, display, request.Password, stringValue(request.TotpSecret), stringValue(request.RecoverySecret), stringValue(request.PlatformSubjectId))
	if err == nil {
		_, err = audit.Write(r.Context(), tx, audit.Event{
			Type: audit.TargetAccountCreated, Actor: audit.ActorOwner, OwnerID: owner.OwnerID,
			RetentionScopeID: item.Id.String(), EntityType: "target_account", EntityID: item.Id.String(),
			Outcome: audit.OutcomeSucceeded, CorrelationID: correlation(r),
			Details: audit.WorkspaceDetails{Result: "created"}, IdempotencyKey: item.Id.String() + ":created",
		})
	}
	if err != nil {
		h.targetConflict(w, r, owner, "target_account.create", err)
		return
	}
	token, idle, err := h.rotateSessionTx(r.Context(), tx, owner, "session_revocation", r)
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		h.targetFailure(w, r, err)
		return
	}
	auth.SetSessionCookie(w, token, idle, h.secureCookies)
	setETag(w, item.Version)
	writeJSON(w, http.StatusCreated, item)
}

func validTargetInput(identifier, display, password, totp, recovery, subject string) bool {
	return validLength(identifier, 1, 254) && validLength(display, 1, 120) &&
		validLength(password, 1, 1024) && utf8.RuneCountInString(totp) <= 1024 &&
		utf8.RuneCountInString(recovery) <= 4096 && utf8.RuneCountInString(subject) <= 255
}

func (h *OwnerAuthHandler) insertTargetAccountTx(r *http.Request, tx pgx.Tx, identifier, display, password, totp, recovery, subject string) (ownerapi.TargetAccount, error) {
	normalized, keyVersion, fingerprint, err := identity.Fingerprint(h.keyRing, identity.TargetLogin, identifier)
	if err != nil {
		return ownerapi.TargetAccount{}, err
	}
	var id string
	err = tx.QueryRow(r.Context(), `INSERT INTO tsw_target_accounts (identifier,identifier_hmac,identifier_key_version,display_label) VALUES ($1,$2,$3,$4) RETURNING id`, normalized, fingerprint[:], keyVersion, display).Scan(&id)
	if err != nil {
		return ownerapi.TargetAccount{}, err
	}
	sealedPassword, sealedTotp, sealedRecovery, status, sealErr := h.sealTargetFields(password, totp, recovery)
	if sealErr != nil {
		return ownerapi.TargetAccount{}, sealErr
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO tsw_target_credentials (target_account_id,password_secret,totp_secret,recovery_secret,platform_subject_id,material_status,materials_sealed) VALUES ($1,$2,$3,$4,NULLIF($5,''),$6,true)`, id, sealedPassword, sealedTotp, sealedRecovery, strings.TrimSpace(subject), status)
	if err != nil {
		return ownerapi.TargetAccount{}, err
	}
	return targetByIDTx(r, tx, id)
}

func targetByIDTx(r *http.Request, tx pgx.Tx, id string) (ownerapi.TargetAccount, error) {
	return scanTargetAccount(tx.QueryRow(r.Context(), `SELECT `+targetProjectionSQL+` FROM tsw_target_accounts target JOIN tsw_target_credentials credentials ON credentials.target_account_id=target.id WHERE target.id=$1`, id))
}

func (h *OwnerAuthHandler) targetByID(r *http.Request, id string) (ownerapi.TargetAccount, error) {
	return scanTargetAccount(h.pool.QueryRow(r.Context(), `SELECT `+targetProjectionSQL+` FROM tsw_target_accounts target JOIN tsw_target_credentials credentials ON credentials.target_account_id=target.id WHERE target.id=$1`, id))
}

func (h *OwnerAuthHandler) getTargetAccount(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticated(w, r, false); !ok {
		return
	}
	item, err := h.targetByID(r, r.PathValue("targetAccountId"))
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "target_not_found", "Not Found", "Target account was not found", 0)
		return
	}
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	detail := ownerapi.TargetAccountDetail{TargetAccount: item, WorkspacePlans: []ownerapi.TargetWorkspacePlan{}}
	rows, err := h.pool.Query(r.Context(), `SELECT workspace.id,workspace.display_name,batch.id,batch.sequence_no,batch.status,batch.planned_at
		FROM tsw_batch_targets selected JOIN tsw_batches batch ON batch.id=selected.batch_id
		JOIN tsw_mother_workspace_bindings binding ON binding.id=batch.binding_id
		JOIN tsw_workspaces workspace ON workspace.id=binding.workspace_id
		WHERE selected.target_account_id=$1 ORDER BY batch.planned_at DESC`, item.Id)
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var plan ownerapi.TargetWorkspacePlan
		if err := rows.Scan(&plan.WorkspaceId, &plan.WorkspaceName, &plan.BatchId, &plan.SequenceNo, &plan.BatchStatus, &plan.PlannedAt); err != nil {
			h.targetFailure(w, r, err)
			return
		}
		detail.WorkspacePlans = append(detail.WorkspacePlans, plan)
	}
	setETag(w, item.Version)
	writeJSON(w, http.StatusOK, detail)
}

func (h *OwnerAuthHandler) updateTargetAccount(w http.ResponseWriter, r *http.Request, params ownerapi.UpdateTargetAccountParams) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	version, ok := ifMatch(params.IfMatch)
	if !ok {
		h.rejectOwnerMutation(w, r, owner, "target_account.update", "invalid_request", http.StatusPreconditionRequired, "if_match_required", "Precondition Required", "If-Match is required")
		return
	}
	var request ownerapi.UpdateTargetAccountJSONRequestBody
	if !decodeJSON(w, r, &request) || !validLength(strings.TrimSpace(request.DisplayLabel), 1, 120) {
		h.rejectOwnerMutation(w, r, owner, "target_account.update", "invalid_request", 422, "invalid_target_account", "Invalid Target Account", "Target account fields are invalid")
		return
	}
	targetID := r.PathValue("targetAccountId")
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	result, err := tx.Exec(r.Context(), `UPDATE tsw_target_accounts SET display_label=$3,status=$4,updated_at=now(),version=version+1 WHERE id=$1 AND version=$2`, targetID, version, strings.TrimSpace(request.DisplayLabel), request.Status)
	if err == nil && result.RowsAffected() != 1 {
		h.rejectOwnerMutation(w, r, owner, "target_account.update", "version_mismatch", 412, "version_mismatch", "Precondition Failed", "The target account changed")
		return
	}
	secretRotation := request.Password != nil || request.TotpSecret != nil || request.RecoverySecret != nil
	if (request.Password != nil && stringValue(request.Password) == "") ||
		(request.RecoverySecret != nil && stringValue(request.RecoverySecret) == "") {
		h.rejectOwnerMutation(w, r, owner, "target_account.update", "invalid_request", 422, "invalid_target_account", "Invalid Target Account", "Secret material cannot be empty")
		return
	}
	credentialChanged := secretRotation || request.PlatformSubjectId != nil
	if err == nil && credentialChanged {
		var sealedPassword, sealedTotp, sealedRecovery []byte
		status := ""
		if request.Password != nil {
			sealedPassword, err = targetdomain.SealMaterial(*request.Password, h.keyRing)
		}
		if err == nil && request.TotpSecret != nil {
			sealedTotp, err = targetdomain.SealMaterial(*request.TotpSecret, h.keyRing)
			status = targetMaterialStatus(*request.TotpSecret)
		}
		if err == nil && request.RecoverySecret != nil {
			sealedRecovery, err = targetdomain.SealMaterial(*request.RecoverySecret, h.keyRing)
		}
		if err != nil {
			h.targetFailure(w, r, err)
			return
		}
		secretRevision := "secret_revision"
		probeReset := ""
		if secretRotation {
			secretRevision = "secret_revision+1"
			probeReset = `,latest_probe_status=NULL,latest_probe_http_status=NULL,latest_probe_error_code=NULL,latest_probe_endpoint_key=NULL,latest_probe_origin=NULL,latest_probed_at=NULL,last_verified_at=NULL`
		}
		_, err = tx.Exec(r.Context(), `UPDATE tsw_target_credentials SET
			password_secret=CASE WHEN $6 THEN $2 ELSE password_secret END,
			totp_secret=CASE WHEN $7 THEN $3 ELSE totp_secret END,
			recovery_secret=CASE WHEN $8 THEN $4 ELSE recovery_secret END,
			material_status=CASE WHEN $7 THEN $9 ELSE material_status END,
			materials_sealed=true,
			platform_subject_id=CASE WHEN $5 <> '' THEN $5 ELSE platform_subject_id END,
			secret_revision=`+secretRevision+probeReset+`,version=version+1 WHERE target_account_id=$1`, targetID,
			sealedPassword, sealedTotp, sealedRecovery, strings.TrimSpace(stringValue(request.PlatformSubjectId)), request.Password != nil, request.TotpSecret != nil, request.RecoverySecret != nil, status)
	}
	item, itemErr := targetByIDTx(r, tx, targetID)
	if err == nil {
		err = itemErr
	}
	if err == nil {
		event, outcome := audit.TargetAccountUpdated, "updated"
		if credentialChanged {
			event, outcome = audit.TargetCredentialsUpdated, "credentials_updated"
		}
		_, err = audit.Write(r.Context(), tx, audit.Event{Type: event, Actor: audit.ActorOwner, OwnerID: owner.OwnerID, RetentionScopeID: targetID, EntityType: "target_account", EntityID: targetID, Outcome: audit.OutcomeSucceeded, CorrelationID: correlation(r), Details: audit.WorkspaceDetails{Result: outcome}, IdempotencyKey: targetID + ":updated:" + strconv.FormatInt(item.Version, 10)})
	}
	var token string
	var idle time.Time
	if err == nil && secretRotation {
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
	if secretRotation {
		auth.SetSessionCookie(w, token, idle, h.secureCookies)
	}
	setETag(w, item.Version)
	writeJSON(w, http.StatusOK, item)
}

func (h *OwnerAuthHandler) previewTargetImport(w http.ResponseWriter, r *http.Request, save bool) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	var request ownerapi.TargetAccountImportRequest
	if !decodeJSON(w, r, &request) {
		h.rejectOwnerMutation(w, r, owner, "target_account.import", "invalid_request", 422, "invalid_import", "Invalid Import", "Import content is invalid")
		return
	}
	rows, err := targetdomain.ParseImport(request.Content)
	if err != nil {
		h.rejectOwnerMutation(w, r, owner, "target_account.import", "invalid_request", 422, "invalid_import", "Invalid Import", err.Error())
		return
	}
	for i := range rows {
		_, keyVersion, fingerprint, fingerprintErr := identity.Fingerprint(h.keyRing, identity.TargetLogin, rows[i].Identifier)
		if fingerprintErr != nil {
			h.targetFailure(w, r, fingerprintErr)
			return
		}
		err = h.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM tsw_target_accounts WHERE identifier_key_version=$1 AND identifier_hmac=$2)`, keyVersion, fingerprint[:]).Scan(&rows[i].Existing)
		if err != nil {
			h.targetFailure(w, r, err)
			return
		}
	}
	if !save {
		preview := ownerapi.TargetAccountImportPreview{Items: []ownerapi.TargetAccountImportRow{}, Total: len(rows)}
		for _, row := range rows {
			preview.Items = append(preview.Items, ownerapi.TargetAccountImportRow{Line: row.Line, Identifier: row.Identifier, DisplayLabel: row.DisplayLabel, HasPassword: row.Password != "", HasTotp: row.TOTPSecret != "", HasRecovery: row.RecoverySecret != "", Existing: row.Existing})
			if row.Existing {
				preview.ExistingCount++
			} else {
				preview.NewCount++
			}
		}
		writeJSON(w, http.StatusOK, preview)
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	result := ownerapi.TargetAccountImportResult{}
	for _, row := range rows {
		if row.Existing {
			_, keyVersion, fingerprint, fingerprintErr := identity.Fingerprint(h.keyRing, identity.TargetLogin, row.Identifier)
			if fingerprintErr != nil {
				h.targetFailure(w, r, fingerprintErr)
				return
			}
			var targetID string
			if err = tx.QueryRow(r.Context(), `SELECT id FROM tsw_target_accounts WHERE identifier_key_version=$1 AND identifier_hmac=$2 FOR UPDATE`, keyVersion, fingerprint[:]).Scan(&targetID); err != nil {
				h.targetConflict(w, r, owner, "target_account.import", err)
				return
			}
			_, err = tx.Exec(r.Context(), `UPDATE tsw_target_accounts SET display_label=$2,updated_at=now(),version=version+1 WHERE id=$1`, targetID, row.DisplayLabel)
			sealedPassword, sealedTotp, sealedRecovery, status, sealErr := h.sealTargetFields(row.Password, row.TOTPSecret, row.RecoverySecret)
			if sealErr != nil {
				h.targetFailure(w, r, sealErr)
				return
			}
			if err == nil {
				_, err = tx.Exec(r.Context(), `UPDATE tsw_target_credentials SET
					password_secret=$2,
					totp_secret=CASE WHEN $3::bytea IS NOT NULL THEN $3 ELSE totp_secret END,
					recovery_secret=CASE WHEN $4::bytea IS NOT NULL THEN $4 ELSE recovery_secret END,
					material_status=CASE WHEN $3::bytea IS NOT NULL THEN $6 ELSE material_status END,
					materials_sealed=true,
					platform_subject_id=CASE WHEN $5 <> '' THEN $5 ELSE platform_subject_id END,
					secret_revision=secret_revision+1,
					latest_probe_status=NULL,latest_probe_http_status=NULL,latest_probe_error_code=NULL,latest_probe_endpoint_key=NULL,latest_probe_origin=NULL,latest_probed_at=NULL,last_verified_at=NULL,
					version=version+1 WHERE target_account_id=$1`, targetID, sealedPassword, sealedTotp, sealedRecovery, row.PlatformSubjectID, status)
			}
			if err == nil {
				_, err = audit.Write(r.Context(), tx, audit.Event{Type: audit.TargetCredentialsUpdated, Actor: audit.ActorOwner, OwnerID: owner.OwnerID, RetentionScopeID: targetID, EntityType: "target_account", EntityID: targetID, Outcome: audit.OutcomeSucceeded, CorrelationID: correlation(r), Details: audit.WorkspaceDetails{Result: "credentials_updated"}, IdempotencyKey: targetImportAuditKey(targetID, correlation(r), row.Line)})
			}
			if err != nil {
				h.targetConflict(w, r, owner, "target_account.import", err)
				return
			}
			result.Existing++
			continue
		}
		item, insertErr := h.insertTargetAccountTx(r, tx, row.Identifier, row.DisplayLabel, row.Password, row.TOTPSecret, row.RecoverySecret, row.PlatformSubjectID)
		if insertErr != nil {
			h.targetConflict(w, r, owner, "target_account.import", insertErr)
			return
		}
		_, err = audit.Write(r.Context(), tx, audit.Event{Type: audit.TargetAccountCreated, Actor: audit.ActorOwner, OwnerID: owner.OwnerID, RetentionScopeID: item.Id.String(), EntityType: "target_account", EntityID: item.Id.String(), Outcome: audit.OutcomeSucceeded, CorrelationID: correlation(r), Details: audit.WorkspaceDetails{Result: "created"}, IdempotencyKey: item.Id.String() + ":imported"})
		if err != nil {
			h.targetFailure(w, r, err)
			return
		}
		result.Created++
	}
	token, idle, err := h.rotateSessionTx(r.Context(), tx, owner, "session_revocation", r)
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		h.targetFailure(w, r, err)
		return
	}
	auth.SetSessionCookie(w, token, idle, h.secureCookies)
	writeJSON(w, http.StatusOK, result)
}

func (h *OwnerAuthHandler) createTargetProbes(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	var request ownerapi.CreateTargetAccountProbesJSONRequestBody
	if !decodeJSON(w, r, &request) || !validLength(request.IdempotencyKey, 8, 128) || !validTargetProbeFilter(stringValueEnum(request.ProbeStatus)) {
		h.rejectOwnerMutation(w, r, owner, "target_probe.create", "invalid_request", 422, "invalid_target_probe", "Invalid Probe", "Probe scope is invalid")
		return
	}
	ids := make([]uuid.UUID, 0)
	if request.TargetAccountIds != nil {
		ids = append(ids, (*request.TargetAccountIds)...)
	}
	rows, err := h.pool.Query(r.Context(), `SELECT target.id FROM tsw_target_accounts target JOIN tsw_target_credentials credentials ON credentials.target_account_id=target.id
		WHERE (cardinality($1::uuid[])=0 OR target.id=ANY($1::uuid[])) AND ($2='' OR target.status=$2)
		AND ($3='' OR ($3='unprobed' AND credentials.latest_probe_status IS NULL) OR credentials.latest_probe_status=$3)
		AND ($4='' OR target.identifier ILIKE '%'||$4||'%' OR target.display_label ILIKE '%'||$4||'%') ORDER BY target.id LIMIT 10000`, ids, stringValueEnum(request.Status), stringValueEnum(request.ProbeStatus), strings.TrimSpace(stringValue(request.Search)))
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	defer rows.Close()
	response := ownerapi.TargetProbeBatch{Items: []ownerapi.TargetProbeStatus{}}
	for rows.Next() {
		var targetID string
		if rows.Scan(&targetID) != nil {
			h.targetFailure(w, r, err)
			return
		}
		item, _, err := h.workspaceTasks.CreateTargetProbe(r.Context(), targetID, targetProbeDedupeKey(request.IdempotencyKey, targetID), correlation(r))
		if err != nil {
			h.targetConflict(w, r, owner, "target_probe.create", err)
			return
		}
		status, err := targetProbeResponse(item)
		if err != nil {
			h.targetFailure(w, r, err)
			return
		}
		response.Items = append(response.Items, status)
	}
	response.Total = len(response.Items)
	writeJSON(w, http.StatusAccepted, response)
}

func (h *OwnerAuthHandler) getTargetProbe(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticated(w, r, false); !ok {
		return
	}
	item, err := h.workspaceTasks.Get(r.Context(), r.PathValue("probeId"))
	if errors.Is(err, pgx.ErrNoRows) || err == nil && item.TaskType != "target_account_probe" {
		writeProblem(w, r, 404, "target_probe_not_found", "Not Found", "Probe status was not found", 0)
		return
	}
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	response, err := targetProbeResponse(item)
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func targetProbeDedupeKey(idempotencyKey, targetID string) string {
	digest := sha256.Sum256([]byte(idempotencyKey + ":" + targetID))
	return "target-probe:" + hex.EncodeToString(digest[:])
}

func targetImportAuditKey(targetID, correlationID string, line int) string {
	digest := sha256.Sum256([]byte(targetID + ":" + correlationID + ":" + strconv.Itoa(line)))
	return "target-import:" + hex.EncodeToString(digest[:])
}

func targetProbeResponse(item task.Task) (ownerapi.TargetProbeStatus, error) {
	id, err := uuid.Parse(item.ID)
	if err != nil {
		return ownerapi.TargetProbeStatus{}, err
	}
	targetID, err := uuid.Parse(item.TargetAccountID)
	if err != nil {
		return ownerapi.TargetProbeStatus{}, err
	}
	result := ownerapi.TargetProbeStatus{Id: id, TargetAccountId: targetID, Status: ownerapi.TargetProbeStatusStatus(item.Status), CreatedAt: item.CreatedAt, FinishedAt: item.FinishedAt}
	if item.Status == "queued" || item.Status == "running" || item.Status == "retry_wait" {
		retry := 2
		result.RetryAfterSeconds = &retry
	}
	return result, nil
}

func stringValueEnum[T ~string](value *T) string {
	if value == nil {
		return ""
	}
	return string(*value)
}

func (h *OwnerAuthHandler) targetConflict(w http.ResponseWriter, r *http.Request, owner ownerContext, operation string, err error) {
	if errors.Is(err, task.ErrIdempotencyConflict) {
		h.rejectOwnerMutation(w, r, owner, operation, "idempotency_conflict", 409, "idempotency_conflict", "Conflict", "The key belongs to a different target")
		return
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			h.rejectOwnerMutation(w, r, owner, operation, "conflict", 409, "target_conflict", "Conflict", "The target account already exists")
			return
		case "23503", "23514":
			h.rejectOwnerMutation(w, r, owner, operation, "invalid_request", 422, "invalid_target_account", "Invalid Target Account", "Target account fields are invalid")
			return
		}
	}
	h.targetFailure(w, r, err)
}

// targetFailure 把底层错误记入服务端日志（带 request_id），对外只写固定 5xx problem。
func (h *OwnerAuthHandler) targetFailure(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("target_data_unavailable", "request_id", RequestIDFromContext(r.Context()), "error", err)
	writeProblem(w, r, 500, "target_accounts_unavailable", "Internal Server Error", "Target account data is temporarily unavailable", 0)
}
