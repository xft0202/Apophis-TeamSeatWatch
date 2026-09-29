package runtime

import (
	"bufio"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/audit"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/identity"
)

func materialString(value string) *string { return &value }

type parsedMotherMaterial struct {
	identifier string
	password   string
	totp       string
}

// parseMotherMaterialTXT deliberately accepts incomplete TOTP material. An
// owner can save the account now and repair the missing material later; this
// parser never turns that state into a verified platform connection.
func parseMotherMaterialTXT(content string) ([]parsedMotherMaterial, []ownerapi.MotherAccountImportRow) {
	materials := make([]parsedMotherMaterial, 0)
	rows := make([]ownerapi.MotherAccountImportRow, 0)
	scanner := bufio.NewScanner(strings.NewReader(content))
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSuffix(scanner.Text(), "\r")
		parts := strings.Split(line, "----")
		row := ownerapi.MotherAccountImportRow{Line: lineNumber}
		if len(parts) != 3 {
			row.Status = ownerapi.MotherAccountImportRowStatusInvalid
			row.Message = materialString("需要账号----密码----2FA三项")
			rows = append(rows, row)
			materials = append(materials, parsedMotherMaterial{})
			continue
		}
		identifier, password, totp := strings.TrimSpace(parts[0]), parts[1], strings.TrimSpace(parts[2])
		if !validLength(identifier, 1, 254) || strings.ContainsAny(identifier, "\r\n") ||
			!validLength(password, 1, 1024) {
			row.Status = ownerapi.MotherAccountImportRowStatusInvalid
			row.Message = materialString("账号或密码无效")
			rows = append(rows, row)
			materials = append(materials, parsedMotherMaterial{})
			continue
		}
		row.Identifier = &identifier
		if totp == "" {
			row.Status = ownerapi.MotherAccountImportRowStatusNeedsTotp
			row.Message = materialString("2FA待补")
		} else if _, err := auth.TOTPCode(totp, timeNow()); err != nil {
			row.Status = ownerapi.MotherAccountImportRowStatusNeedsTotp
			row.Message = materialString("2FA格式待修正")
		} else {
			row.Status = ownerapi.MotherAccountImportRowStatusImported
		}
		rows = append(rows, row)
		materials = append(materials, parsedMotherMaterial{identifier: identifier, password: password, totp: totp})
	}
	return materials, rows
}

// timeNow is a variable to keep material parsing deterministic in tests that
// need to validate a syntactically valid TOTP secret.
var timeNow = func() time.Time { return time.Now() }

func (h *OwnerAuthHandler) importMotherAccounts(w http.ResponseWriter, r *http.Request, _ ownerapi.ImportMotherAccountsParams) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	var request ownerapi.ImportMotherAccountsJSONRequestBody
	if !decodeJSON(w, r, &request) || len(request.Content) > 10*1024*1024 {
		h.rejectOwnerMutation(w, r, owner, "mother_account.import", "invalid_request", http.StatusUnprocessableEntity, "invalid_materials", "Invalid Materials", "资料文件无效")
		return
	}
	parsed, rows := parseMotherMaterialTXT(request.Content)
	result := ownerapi.MotherAccountImportResult{Rows: rows}
	if len(result.Rows) == 0 {
		writeJSON(w, http.StatusOK, result)
		return
	}

	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	seen := make(map[string]struct{})
	importedIDs := make([]string, 0)
	changed := false
	for index := range result.Rows {
		row := &result.Rows[index]
		material := parsed[index]
		switch row.Status {
		case ownerapi.MotherAccountImportRowStatusInvalid:
			result.Invalid++
			continue
		}
		identifier := material.identifier
		key, keyVersion, fingerprint, fingerprintErr := identity.Fingerprint(h.keyRing, identity.MotherLogin, identifier)
		if fingerprintErr != nil {
			row.Status = ownerapi.MotherAccountImportRowStatusInvalid
			row.Message = materialString("账号无效")
			result.Invalid++
			continue
		}
		if _, exists := seen[key]; exists {
			row.Status = ownerapi.MotherAccountImportRowStatusDuplicate
			row.Message = materialString("文件内重复")
			result.Duplicate++
			continue
		}
		seen[key] = struct{}{}
		var existing string
		err = tx.QueryRow(r.Context(), `SELECT mother_account_id FROM tsw_mother_account_credentials WHERE identifier_key_version=$1 AND identifier_hmac=$2`, keyVersion, fingerprint[:]).Scan(&existing)
		if err == nil {
			row.Status = ownerapi.MotherAccountImportRowStatusDuplicate
			row.Message = materialString("资料已存在")
			result.Duplicate++
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			h.workspaceFailure(w, r, err)
			return
		}
		var accountID string
		err = tx.QueryRow(r.Context(), `INSERT INTO tsw_mother_accounts (display_name) VALUES ($1) RETURNING id`, identifier).Scan(&accountID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO tsw_mother_account_credentials (mother_account_id,login_identifier,identifier_hmac,identifier_key_version,password_secret,totp_secret) VALUES ($1,$2,$3,$4,$5,NULLIF($6,'')::bytea)`, accountID, identifier, fingerprint[:], keyVersion, []byte(material.password), []byte(material.totp))
		}
		if err != nil {
			h.workspaceFailure(w, r, err)
			return
		}
		changed = true
		importedIDs = append(importedIDs, accountID)
		result.Imported++
	}
	var token string
	var idle time.Time
	if changed {
		for _, accountID := range importedIDs {
			if _, err = audit.Write(r.Context(), tx, audit.Event{Type: audit.MotherAccountCreated, Actor: audit.ActorOwner, OwnerID: owner.OwnerID, EntityType: "mother_account", EntityID: accountID, Outcome: audit.OutcomeSucceeded, CorrelationID: correlation(r), Details: audit.WorkspaceDetails{Result: "imported"}, IdempotencyKey: accountID + ":mother-import"}); err != nil {
				h.workspaceFailure(w, r, err)
				return
			}
		}
		token, idle, err = h.rotateSessionTx(r.Context(), tx, owner, "session_revocation", r)
		if err != nil {
			h.workspaceFailure(w, r, err)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	if changed {
		auth.SetSessionCookie(w, token, idle, h.secureCookies)
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *OwnerAuthHandler) exportMotherAccounts(w http.ResponseWriter, r *http.Request, _ ownerapi.ExportMotherAccountsParams) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	var request ownerapi.ExportMotherAccountsJSONRequestBody
	if !decodeJSON(w, r, &request) || request.ExpectedCount < 1 {
		h.rejectOwnerMutation(w, r, owner, "mother_account.export", "invalid_request", http.StatusUnprocessableEntity, "invalid_export", "Invalid Export", "导出范围无效")
		return
	}
	if !request.Confirmed {
		h.rejectOwnerMutation(w, r, owner, "mother_account.export", "confirmation_required", http.StatusPreconditionRequired, "export_confirmation_required", "Confirmation Required", "请确认导出范围与数量")
		return
	}

	query := `SELECT credential.login_identifier, credential.password_secret, credential.totp_secret
		FROM tsw_mother_account_credentials credential JOIN tsw_mother_accounts account ON account.id=credential.mother_account_id`
	var args []any
	if request.AccountIds != nil {
		query += ` WHERE account.id = ANY($1)`
		args = append(args, *request.AccountIds)
	}
	query += ` ORDER BY credential.login_identifier ASC`
	rows, err := h.pool.Query(r.Context(), query, args...)
	if err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	defer rows.Close()
	var output strings.Builder
	count := 0
	for rows.Next() {
		var identifier string
		var password, totp []byte
		if err = rows.Scan(&identifier, &password, &totp); err != nil {
			h.workspaceFailure(w, r, err)
			return
		}
		_, _ = fmt.Fprintf(&output, "%s----%s----%s\n", identifier, password, totp)
		count++
	}
	if err = rows.Err(); err != nil {
		h.workspaceFailure(w, r, err)
		return
	}
	if count != request.ExpectedCount {
		h.rejectOwnerMutation(w, r, owner, "mother_account.export", "range_changed", http.StatusConflict, "export_range_changed", "Export Range Changed", "导出范围已变化，请重新确认")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="mother-account-materials.txt"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(output.String()))
}
