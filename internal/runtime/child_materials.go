package runtime

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/audit"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/identity"
	targetdomain "github.com/xft0202/Apophis-TeamSeatWatch/internal/target"
)

type childMaterial struct{ identifier, password, totp string }

const maxChildMaterialsContent = 10 * 1024 * 1024

func decodeChildMaterialsImport(w http.ResponseWriter, r *http.Request, request *ownerapi.ImportChildMaterialsJSONRequestBody) bool {
	// A one-byte control character may occupy six bytes as a JSON escape.
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 6*maxChildMaterialsContent+1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(request) != nil || len(request.Content) > maxChildMaterialsContent {
		return false
	}
	var trailing any
	return errors.Is(decoder.Decode(&trailing), io.EOF)
}

func parseChildMaterials(content string) ([]childMaterial, []ownerapi.ChildMaterialsImportRow) {
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 4096), maxChildMaterialsContent+1)
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

func (h *OwnerAuthHandler) ImportChildMaterials(w http.ResponseWriter, r *http.Request, _ ownerapi.ImportChildMaterialsParams) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	var request ownerapi.ImportChildMaterialsJSONRequestBody
	if !decodeChildMaterialsImport(w, r, &request) {
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
	for i := range result.Rows {
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
		// A competing request can insert after the duplicate read. Isolate this
		// row so a unique-key race reports duplicate without discarding other rows.
		if _, err = tx.Exec(r.Context(), `SAVEPOINT child_material_row`); err != nil {
			h.targetFailure(w, r, err)
			return
		}
		item, insertErr := h.insertTargetAccountTx(r, tx, material.identifier, material.identifier, material.password, material.totp, "", "")
		if insertErr != nil {
			var pgErr *pgconn.PgError
			if errors.As(insertErr, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "tsw_target_accounts_identifier_uq" {
				if _, err = tx.Exec(r.Context(), `ROLLBACK TO SAVEPOINT child_material_row`); err == nil {
					_, err = tx.Exec(r.Context(), `RELEASE SAVEPOINT child_material_row`)
				}
				if err != nil {
					h.targetFailure(w, r, err)
					return
				}
				row.Status, row.Message = ownerapi.ChildMaterialsImportRowStatusDuplicate, materialString("资料已存在")
				result.Duplicate++
				continue
			}
			h.targetConflict(w, r, owner, "child_material.import", insertErr)
			return
		}
		if _, err = tx.Exec(r.Context(), `RELEASE SAVEPOINT child_material_row`); err != nil {
			h.targetFailure(w, r, err)
			return
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
	case ownerapi.Selected:
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
	case ownerapi.Filtered:
		if request.AccountIds != nil || len(strings.TrimSpace(stringValue(request.Search))) > 254 {
			h.rejectOwnerMutation(w, r, owner, "child_material.export", "invalid_request", 422, "invalid_export", "Invalid Export", "导出范围无效")
			return
		}
		query += `($1='' OR target.identifier ILIKE '%'||$1||'%' OR target.display_label ILIKE '%'||$1||'%') `
		args = append(args, strings.TrimSpace(stringValue(request.Search)))
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
