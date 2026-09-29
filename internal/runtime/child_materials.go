package runtime

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/audit"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/identity"
	targetdomain "github.com/xft0202/Apophis-TeamSeatWatch/internal/target"
)

type childMaterial struct{ identifier, password, totp string }

func parseChildMaterials(content string) ([]childMaterial, []ownerapi.ChildMaterialsImportRow) {
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 4096), 10*1024*1024+1)
	var materials []childMaterial
	var rows []ownerapi.ChildMaterialsImportRow
	for line := 1; scanner.Scan(); line++ {
		raw := strings.TrimSuffix(scanner.Text(), "\r")
		fields := strings.Split(raw, "----")
		row := ownerapi.ChildMaterialsImportRow{Line: line}
		material := childMaterial{}
		if len(fields) != 3 {
			row.Status, row.Message = ownerapi.ChildMaterialsImportRowStatusInvalid, materialString("需要账号----密码----2FA三项")
		} else {
			identifier := strings.ToLower(strings.TrimSpace(fields[0]))
			address, err := mail.ParseAddress(identifier)
			if err != nil || address.Address != identifier || !validLength(identifier, 1, 254) || !validLength(fields[1], 1, 1024) || len(fields[2]) > 1024 || strings.ContainsAny(fields[1], "\r\n") {
				row.Status, row.Message = ownerapi.ChildMaterialsImportRowStatusInvalid, materialString("账号或密码无效")
			} else {
				row.Identifier = &identifier
				material = childMaterial{identifier, fields[1], strings.TrimSpace(fields[2])}
				row.Status = ownerapi.ChildMaterialsImportRowStatusImported
				if !targetdomain.CompleteTOTP(material.totp) {
					row.Status, row.Message = ownerapi.ChildMaterialsImportRowStatusNeedsTotp, materialString("2FA资料待补")
				}
			}
		}
		materials = append(materials, material)
		rows = append(rows, row)
	}
	return materials, rows
}

// The TXT contract permits 10 MiB of request bytes, unlike ordinary Owner JSON.
func decodeChildImportJSON(w http.ResponseWriter, r *http.Request, request *ownerapi.ImportChildMaterialsJSONRequestBody) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 10<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(request); err != nil {
		return false
	}
	var trailing any
	return errors.Is(decoder.Decode(&trailing), io.EOF)
}

// ON CONFLICT makes a concurrent import a per-line duplicate without aborting
// the transaction containing other valid lines.
func (h *OwnerAuthHandler) insertChildMaterialTx(r *http.Request, tx pgx.Tx, material childMaterial, keyVersion uint16, fingerprint []byte) (ownerapi.TargetAccount, bool, error) {
	var id string
	err := tx.QueryRow(r.Context(), `INSERT INTO tsw_target_accounts (identifier,identifier_hmac,identifier_key_version,display_label)
		VALUES ($1,$2,$3,$1) ON CONFLICT ON CONSTRAINT tsw_target_accounts_identifier_uq DO NOTHING RETURNING id`, material.identifier, fingerprint, keyVersion).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ownerapi.TargetAccount{}, false, nil
	}
	if err != nil {
		return ownerapi.TargetAccount{}, false, err
	}
	password, totp, _, status, err := h.sealTargetFields(material.password, material.totp, "")
	if err != nil {
		return ownerapi.TargetAccount{}, false, err
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO tsw_target_credentials (target_account_id,password_secret,totp_secret,material_status,materials_sealed)
		VALUES ($1,$2,$3,$4,true)`, id, password, totp, status)
	if err != nil {
		return ownerapi.TargetAccount{}, false, err
	}
	item, err := targetByIDTx(r, tx, id)
	return item, true, err
}

func (h *OwnerAuthHandler) ImportChildMaterials(w http.ResponseWriter, r *http.Request, _ ownerapi.ImportChildMaterialsParams) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	var request ownerapi.ImportChildMaterialsJSONRequestBody
	if !decodeChildImportJSON(w, r, &request) || len(request.Content) > 10*1024*1024 {
		h.rejectOwnerMutation(w, r, owner, "child_material.import", "invalid_request", 422, "invalid_materials", "Invalid Materials", "资料文件无效")
		return
	}
	materials, rows := parseChildMaterials(request.Content)
	result := ownerapi.ChildMaterialsImportResult{Rows: rows}
	if len(rows) == 0 {
		writeJSON(w, 200, result)
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	seen := make(map[string]bool)
	changed := false
	// Acquire unique-key insert locks in a consistent order across concurrent
	// imports; feedback still refers to the original physical line numbers.
	indices := make([]int, len(result.Rows))
	for i := range indices {
		indices[i] = i
	}
	sort.SliceStable(indices, func(a, b int) bool {
		return materials[indices[a]].identifier < materials[indices[b]].identifier
	})
	for _, i := range indices {
		row := &result.Rows[i]
		if row.Status == ownerapi.ChildMaterialsImportRowStatusInvalid {
			result.Invalid++
			continue
		}
		material := materials[i]
		_, version, fingerprint, fingerprintErr := identity.Fingerprint(h.keyRing, identity.TargetLogin, material.identifier)
		if fingerprintErr != nil {
			h.targetFailure(w, r, fingerprintErr)
			return
		}
		key := fmt.Sprintf("%d:%x", version, fingerprint)
		if seen[key] {
			row.Status, row.Message = ownerapi.ChildMaterialsImportRowStatusDuplicate, materialString("文件内重复")
			result.Duplicate++
			continue
		}
		seen[key] = true
		var existing string
		err = tx.QueryRow(r.Context(), `SELECT id FROM tsw_target_accounts WHERE identifier_key_version=$1 AND identifier_hmac=$2`, version, fingerprint[:]).Scan(&existing)
		if err == nil {
			row.Status, row.Message = ownerapi.ChildMaterialsImportRowStatusDuplicate, materialString("资料已存在")
			result.Duplicate++
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			h.targetFailure(w, r, err)
			return
		}
		item, inserted, insertErr := h.insertChildMaterialTx(r, tx, material, version, fingerprint[:])
		if insertErr != nil {
			h.targetConflict(w, r, owner, "child_material.import", insertErr)
			return
		}
		if !inserted {
			row.Status, row.Message = ownerapi.ChildMaterialsImportRowStatusDuplicate, materialString("资料已存在")
			result.Duplicate++
			continue
		}
		_, err = audit.Write(r.Context(), tx, audit.Event{Type: audit.TargetAccountCreated, Actor: audit.ActorOwner, OwnerID: owner.OwnerID, RetentionScopeID: item.Id.String(), EntityType: "target_account", EntityID: item.Id.String(), Outcome: audit.OutcomeSucceeded, CorrelationID: correlation(r), Details: audit.WorkspaceDetails{Result: "created"}, IdempotencyKey: item.Id.String() + ":child-material"})
		if err != nil {
			h.targetFailure(w, r, err)
			return
		}
		changed = true
		result.Imported++
	}
	var token string
	var idleTime = owner.IdleExpiresAt
	if changed {
		token, idleTime, err = h.rotateSessionTx(r.Context(), tx, owner, "session_revocation", r)
		if err != nil {
			h.targetFailure(w, r, err)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		h.targetFailure(w, r, err)
		return
	}
	if changed {
		auth.SetSessionCookie(w, token, idleTime, h.secureCookies)
	}
	writeJSON(w, 200, result)
}

func (h *OwnerAuthHandler) ExportChildMaterials(w http.ResponseWriter, r *http.Request, _ ownerapi.ExportChildMaterialsParams) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	var request ownerapi.ExportChildMaterialsJSONRequestBody
	if !decodeJSON(w, r, &request) || request.ExpectedCount < 1 || !request.Confirmed {
		h.rejectOwnerMutation(w, r, owner, "child_material.export", "invalid_request", 428, "export_confirmation_required", "Confirmation Required", "请确认导出范围与数量")
		return
	}
	query := `SELECT target.identifier,credentials.password_secret,credentials.totp_secret FROM tsw_target_accounts target JOIN tsw_target_credentials credentials ON credentials.target_account_id=target.id WHERE `
	var args []any
	switch request.Scope {
	case ownerapi.ChildMaterialsExportScopeSelected:
		if request.AccountIds == nil || len(*request.AccountIds) == 0 || len(*request.AccountIds) > 10000 || request.Search != nil {
			h.rejectOwnerMutation(w, r, owner, "child_material.export", "invalid_request", 422, "invalid_export", "Invalid Export", "导出范围无效")
			return
		}
		seen := make(map[string]bool)
		for _, id := range *request.AccountIds {
			if seen[id.String()] {
				h.rejectOwnerMutation(w, r, owner, "child_material.export", "invalid_request", 422, "invalid_export", "Invalid Export", "重复选择")
				return
			}
			seen[id.String()] = true
		}
		query += `target.id=ANY($1) `
		args = append(args, *request.AccountIds)
	case ownerapi.ChildMaterialsExportScopeFiltered:
		if request.AccountIds != nil || len(strings.TrimSpace(stringValue(request.Search))) > 254 {
			h.rejectOwnerMutation(w, r, owner, "child_material.export", "invalid_request", 422, "invalid_export", "Invalid Export", "导出范围无效")
			return
		}
		query += `($1='' OR target.identifier ILIKE '%'||$1||'%' OR target.display_label ILIKE '%'||$1||'%') `
		args = append(args, strings.ToLower(strings.TrimSpace(stringValue(request.Search))))
	default:
		h.rejectOwnerMutation(w, r, owner, "child_material.export", "invalid_request", 422, "invalid_export", "Invalid Export", "导出范围无效")
		return
	}
	query += `ORDER BY target.identifier ASC`
	rows, err := h.pool.Query(r.Context(), query, args...)
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	var output strings.Builder
	count := 0
	for rows.Next() {
		var identifier string
		var password, totp []byte
		if err = rows.Scan(&identifier, &password, &totp); err != nil {
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
		fmt.Fprintf(&output, "%s----%s----%s\n", identifier, pass, secret)
		count++
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		h.targetFailure(w, r, err)
		return
	}
	if count != request.ExpectedCount {
		h.rejectOwnerMutation(w, r, owner, "child_material.export", "conflict", 409, "export_range_changed", "Export Range Changed", "导出范围已变化，请重新确认")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="child-account-materials.txt"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(output.String()))
}
