//go:build integration

package runtime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/migrations"
	targetdomain "github.com/xft0202/Apophis-TeamSeatWatch/internal/target"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/task"
)

func TestChildMaterialsOwnerContractIntegration(t *testing.T) {
	dsn := os.Getenv("TSW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TSW_TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err = migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ring := cardIntegrationKeyRing{}
	origins, err := auth.ParseOriginPolicy("https://owner.test")
	if err != nil {
		t.Fatal(err)
	}
	handler := &OwnerAuthHandler{pool: pool, keyRing: ring, origins: origins}
	ids, session, csrf := seedCardActivationGraph(t, ctx, pool)
	// Convert the preexisting plaintext fixture before serving Owner endpoints.
	if err := sealExistingTargetMaterials(ctx, pool, ring); err != nil {
		t.Fatal(err)
	}
	// A real fingerprinted preexisting account must be recognized as a duplicate.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = handler.insertTargetAccountTx(httptest.NewRequest("POST", "/", nil), tx, "existing@example.com", "existing@example.com", "pw-existing", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// The graph fixture also contains an older target account.
	content := "one@example.com----pw-one----JBSWY3DPEHPK3PXP\none@example.com----pw-two----\nwaiting@example.com----pw-three----broken\nexisting@example.com----pw-four----\nmalformed"
	invoke := func(method, path string, body any, sessionValue string, csrfValid bool) *httptest.ResponseRecorder {
		t.Helper()
		payload, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(payload))
		req.Header.Set("Origin", "https://owner.test")
		if csrfValid {
			req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
			req.Header.Set(auth.CSRFHeaderName, csrf)
		}
		if sessionValue != "" {
			req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionValue})
		}
		recorder := httptest.NewRecorder()
		if strings.HasSuffix(path, "/import") {
			handler.ImportChildMaterials(recorder, req, ownerapi.ImportChildMaterialsParams{})
		} else {
			handler.ExportChildMaterials(recorder, req, ownerapi.ExportChildMaterialsParams{})
		}
		return recorder
	}
	if got := invoke("POST", "/api/owner/v1/child-materials/import", map[string]string{"content": content}, session, false); got.Code != 403 {
		t.Fatalf("missing CSRF = %d", got.Code)
	}
	if got := invoke("POST", "/api/owner/v1/child-materials/import", map[string]string{"content": content}, "", true); got.Code != 401 {
		t.Fatalf("unauthorized = %d", got.Code)
	}
	if got := invoke("POST", "/api/owner/v1/child-materials/import", map[string]string{"content": strings.Repeat("x", 600<<10)}, session, true); got.Code != 200 || !strings.Contains(got.Body.String(), `"invalid":1`) {
		t.Fatalf("valid large request=%d %s", got.Code, got.Body.String())
	}
	imported := invoke("POST", "/api/owner/v1/child-materials/import", map[string]string{"content": content}, session, true)
	if imported.Code != 200 {
		t.Fatalf("import=%d %s", imported.Code, imported.Body.String())
	}
	var result struct {
		Imported, Duplicate, Invalid int
		Rows                         []struct {
			Line   int
			Status string
		}
	}
	if err = json.Unmarshal(imported.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Imported != 2 || result.Duplicate != 2 || result.Invalid != 1 || len(result.Rows) != 5 || result.Rows[2].Status != "needs_totp" {
		t.Fatalf("import result %+v", result)
	}
	var password, totp []byte
	var status string
	if err = pool.QueryRow(ctx, `SELECT c.password_secret,c.totp_secret,c.material_status FROM tsw_target_accounts a JOIN tsw_target_credentials c ON a.id=c.target_account_id WHERE a.identifier='waiting@example.com'`).Scan(&password, &totp, &status); err != nil {
		t.Fatal(err)
	}
	if status != "needs_totp" || bytes.Contains(password, []byte("pw-three")) || !targetdomain.Sealed(password) {
		t.Fatalf("unsafe saved material: status=%q", status)
	}
	// The import rotates the session; use the newly issued Owner cookie.
	rotated := ""
	for _, cookie := range imported.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName {
			rotated = cookie.Value
		}
	}
	if rotated == "" {
		t.Fatal("sensitive import did not rotate session")
	}
	exportRequest := map[string]any{"scope": "filtered", "search": "@example.com", "expectedCount": 4, "confirmed": true}
	if got := invoke("POST", "/api/owner/v1/child-materials/export", exportRequest, rotated, false); got.Code != 403 {
		t.Fatalf("export missing CSRF=%d", got.Code)
	}
	if got := invoke("POST", "/api/owner/v1/child-materials/export", map[string]any{"scope": "filtered", "search": "@example.com", "expectedCount": 2, "confirmed": true}, rotated, true); got.Code != 409 {
		t.Fatalf("changed count=%d", got.Code)
	}
	exported := invoke("POST", "/api/owner/v1/child-materials/export", exportRequest, rotated, true)
	if exported.Code != 200 {
		t.Fatalf("export=%d %s", exported.Code, exported.Body.String())
	}
	if !strings.Contains(exported.Body.String(), "waiting@example.com----pw-three----broken\n") || strings.Contains(exported.Body.String(), "subject") {
		t.Fatalf("unexpected export: %s", exported.Body.String())
	}
	var waitingID string
	if err = pool.QueryRow(ctx, `SELECT id FROM tsw_target_accounts WHERE identifier='waiting@example.com'`).Scan(&waitingID); err != nil {
		t.Fatal(err)
	}
	store := task.NewStore(pool, ring)
	probe, err := store.TargetProbeTarget(ctx, waitingID)
	if err != nil || probe.Password != "pw-three" {
		t.Fatalf("repairable account cannot be probed: %+v %v", probe, err)
	}
	if got := invoke("POST", "/api/owner/v1/child-materials/export", map[string]any{"scope": "selected", "accountIds": []string{waitingID}, "expectedCount": 1, "confirmed": true}, rotated, true); got.Code != 200 || strings.Count(got.Body.String(), "\n") != 1 {
		t.Fatalf("selected export=%d %s", got.Code, got.Body.String())
	}
	// Filtered export must use the same identifier-or-label search as the list.
	if _, err = pool.Exec(ctx, `UPDATE tsw_target_accounts SET display_label='child alias' WHERE identifier='existing@example.com'`); err != nil {
		t.Fatal(err)
	}
	alias := "child alias"
	listed := httptest.NewRecorder()
	listRequest := httptest.NewRequest("GET", "/api/owner/v1/target-accounts?search=child+alias", nil)
	listRequest.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: rotated})
	handler.listTargetAccounts(listed, listRequest, ownerapi.ListTargetAccountsParams{Search: &alias})
	var aliasList ownerapi.TargetAccountList
	if listed.Code != 200 || json.Unmarshal(listed.Body.Bytes(), &aliasList) != nil || aliasList.Total != 1 {
		t.Fatalf("alias list=%d %s", listed.Code, listed.Body.String())
	}
	if got := invoke("POST", "/api/owner/v1/child-materials/export", map[string]any{"scope": "filtered", "search": alias, "expectedCount": 1, "confirmed": true}, rotated, true); got.Code != 200 || !strings.Contains(got.Body.String(), "existing@example.com----pw-existing----") {
		t.Fatalf("alias export=%d %s", got.Code, got.Body.String())
	}
	// The competing insertion is invisible to the duplicate read. Depending on
	// the active migration, the contender waits at the unique index or the
	// earlier rotation action fence; either must serialize this line and retain
	// the same duplicate result without rolling back the following valid line.
	competing, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = handler.insertTargetAccountTx(httptest.NewRequest("POST", "/", nil), competing, "race@example.com", "race@example.com", "raced-pw", "", "", ""); err != nil {
		t.Fatal(err)
	}
	importDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		importDone <- invoke("POST", "/api/owner/v1/child-materials/import", map[string]string{"content": "race@example.com----pw----\nother@example.com----pw----"}, rotated, true)
	}()
	waiting := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event IN ('transactionid','advisory') AND query LIKE 'INSERT INTO tsw_target_accounts%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
	}
	if err = competing.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if !waiting {
		t.Fatal("competing import never reached the insertion serialization boundary")
	}
	raced := <-importDone
	var raceResult ownerapi.ChildMaterialsImportResult
	if raced.Code != 200 || json.Unmarshal(raced.Body.Bytes(), &raceResult) != nil || raceResult.Duplicate != 1 || raceResult.Imported != 1 {
		t.Fatalf("concurrent import=%d %s", raced.Code, raced.Body.String())
	}
	for _, cookie := range raced.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName {
			rotated = cookie.Value
		}
	}
	var operationID string
	if err = pool.QueryRow(ctx, `SELECT id FROM tsw_operations WHERE workspace_id=$1 AND operation_type='join'`, ids.workspace).Scan(&operationID); err != nil {
		t.Fatal(err)
	}
	operationTargetID, joinTaskID := uuid.New().String(), uuid.New().String()
	if _, err = pool.Exec(ctx, `INSERT INTO tsw_operation_targets(id,operation_id,target_account_id,ordinal) VALUES ($1,$2,$3,2)`, operationTargetID, operationID, waitingID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO tsw_tasks(id,operation_target_id,workspace_id,target_account_id,task_type,dedupe_key,input_snapshot,correlation_id) VALUES ($1,$2,$3,$4,'join','material-join-task','{}','material-join')`, joinTaskID, operationTargetID, ids.workspace, waitingID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.JoinTarget(ctx, task.Task{ID: joinTaskID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("incomplete 2FA allowed join target: %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE tsw_target_credentials SET material_status='needs_totp',version=version+1 WHERE target_account_id=(SELECT target_account_id FROM tsw_batch_memberships WHERE id=$1)`, ids.membership); err != nil {
		t.Fatal(err)
	}
	secret := "TSW1-" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x41}, 20))
	if got := activateCardIntegrationRequest(t, handler, ids.membership, rotated, csrf, secret, "material-card"); got.Code != 409 {
		t.Fatalf("incomplete 2FA allowed card activation: %d %s", got.Code, got.Body.String())
	}
	if _, err = pool.Exec(ctx, `UPDATE tsw_target_credentials SET latest_probe_status='available',latest_probe_endpoint_key='account_usage',latest_probe_origin='worker',latest_probed_at=now(),last_verified_at=now(),version=version+1 WHERE target_account_id=$1`, waitingID); err != nil {
		t.Fatal(err)
	}
	correction, _ := json.Marshal(map[string]string{"displayLabel": "waiting@example.com", "status": "active", "password": "pw-three", "totpSecret": "JBSWY3DPEHPK3PXP"})
	request := httptest.NewRequest("PATCH", "/api/owner/v1/target-accounts/"+waitingID, bytes.NewReader(correction))
	request.SetPathValue("targetAccountId", waitingID)
	request.Header.Set("Origin", "https://owner.test")
	request.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
	request.Header.Set(auth.CSRFHeaderName, csrf)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: rotated})
	updated := httptest.NewRecorder()
	handler.updateTargetAccount(updated, request, ownerapi.UpdateTargetAccountParams{IfMatch: "\"1\""})
	if updated.Code != 200 {
		t.Fatalf("correction=%d %s", updated.Code, updated.Body.String())
	}
	var probeReset bool
	if err = pool.QueryRow(ctx, `SELECT material_status,latest_probe_status IS NULL FROM tsw_target_credentials WHERE target_account_id=$1`, waitingID).Scan(&status, &probeReset); err != nil {
		t.Fatal(err)
	}
	if status != "complete" || !probeReset {
		t.Fatalf("correction did not recheck: status=%q reset=%v", status, probeReset)
	}
	if _, err = store.JoinTarget(ctx, task.Task{ID: joinTaskID}); err == nil {
		t.Fatal("corrected materials alone authorized an operation without premium-seat scope")
	}
	// Long credentials (1024–16384 chars) and session markers work for import, create, and update.
	longCred := strings.Repeat("k", 5200)
	overCred := strings.Repeat("z", 16385)
	longImported := invoke("POST", "/api/owner/v1/child-materials/import", map[string]string{"content": "longchild@example.com----" + longCred + "----cookie\n"}, rotated, true)
	if longImported.Code != 200 {
		t.Fatalf("long child import=%d %s", longImported.Code, longImported.Body.String())
	}
	for _, cookie := range longImported.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName {
			rotated = cookie.Value
		}
	}
	createBody, _ := json.Marshal(map[string]string{"identifier": "createdlong@example.com", "displayLabel": "createdlong@example.com", "password": strings.Repeat("p", 8000), "totpSecret": "google"})
	createReq := httptest.NewRequest("POST", "/api/owner/v1/target-accounts", bytes.NewReader(createBody))
	createReq.Header.Set("Origin", "https://owner.test")
	createReq.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
	createReq.Header.Set(auth.CSRFHeaderName, csrf)
	createReq.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: rotated})
	createdRec := httptest.NewRecorder()
	handler.createTargetAccount(createdRec, createReq)
	if createdRec.Code != 201 {
		t.Fatalf("createTargetAccount long password=%d %s", createdRec.Code, createdRec.Body.String())
	}
	for _, cookie := range createdRec.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName {
			rotated = cookie.Value
		}
	}
	var createdItem ownerapi.TargetAccount
	if err = json.Unmarshal(createdRec.Body.Bytes(), &createdItem); err != nil || createdItem.MaterialStatus != "complete" {
		t.Fatalf("created long target account=%+v err=%v", createdItem, err)
	}
	updateLongBody, _ := json.Marshal(map[string]string{"displayLabel": "createdlong@example.com", "status": "active", "password": strings.Repeat("u", 16384), "totpSecret": "cookie"})
	updateLongReq := httptest.NewRequest("PATCH", "/api/owner/v1/target-accounts/"+createdItem.Id.String(), bytes.NewReader(updateLongBody))
	updateLongReq.SetPathValue("targetAccountId", createdItem.Id.String())
	updateLongReq.Header.Set("Origin", "https://owner.test")
	updateLongReq.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
	updateLongReq.Header.Set(auth.CSRFHeaderName, csrf)
	updateLongReq.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: rotated})
	updatedLongRec := httptest.NewRecorder()
	handler.updateTargetAccount(updatedLongRec, updateLongReq, ownerapi.UpdateTargetAccountParams{IfMatch: `"1"`})
	if updatedLongRec.Code != 200 {
		t.Fatalf("updateTargetAccount 16384 password=%d %s", updatedLongRec.Code, updatedLongRec.Body.String())
	}
	for _, cookie := range updatedLongRec.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName {
			rotated = cookie.Value
		}
	}
	updateOverBody, _ := json.Marshal(map[string]string{"displayLabel": "createdlong@example.com", "status": "active", "password": overCred})
	updateOverReq := httptest.NewRequest("PATCH", "/api/owner/v1/target-accounts/"+createdItem.Id.String(), bytes.NewReader(updateOverBody))
	updateOverReq.SetPathValue("targetAccountId", createdItem.Id.String())
	updateOverReq.Header.Set("Origin", "https://owner.test")
	updateOverReq.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
	updateOverReq.Header.Set(auth.CSRFHeaderName, csrf)
	updateOverReq.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: rotated})
	updatedOverRec := httptest.NewRecorder()
	handler.updateTargetAccount(updatedOverRec, updateOverReq, ownerapi.UpdateTargetAccountParams{IfMatch: `"2"`})
	if updatedOverRec.Code != 422 {
		t.Fatalf("updateTargetAccount >16384 password expected 422, got=%d", updatedOverRec.Code)
	}
	// Also test mother create & update with 1024–16384 credentials
	motherCreateBody, _ := json.Marshal(map[string]string{"displayName": "Long Mother", "loginIdentifier": "longmother@example.com", "password": strings.Repeat("m", 6000), "totpSecret": "google"})
	motherCreateReq := httptest.NewRequest("POST", "/api/owner/v1/mother-accounts", bytes.NewReader(motherCreateBody))
	motherCreateReq.Header.Set("Origin", "https://owner.test")
	motherCreateReq.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
	motherCreateReq.Header.Set(auth.CSRFHeaderName, csrf)
	motherCreateReq.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: rotated})
	motherCreateRec := httptest.NewRecorder()
	handler.createMotherAccount(motherCreateRec, motherCreateReq)
	if motherCreateRec.Code != 201 {
		t.Fatalf("createMotherAccount long password=%d %s", motherCreateRec.Code, motherCreateRec.Body.String())
	}
	for _, cookie := range motherCreateRec.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName {
			rotated = cookie.Value
		}
	}
	var createdMother ownerapi.MotherAccount
	if err = json.Unmarshal(motherCreateRec.Body.Bytes(), &createdMother); err != nil || createdMother.MaterialStatus != ownerapi.MotherAccountMaterialStatusComplete {
		t.Fatalf("createdMother=%+v err=%v", createdMother, err)
	}
	motherUpdateBody, _ := json.Marshal(map[string]string{"displayName": "Long Mother", "status": "active", "password": strings.Repeat("n", 16384), "totpSecret": "cookie"})
	motherUpdateReq := httptest.NewRequest("PATCH", "/api/owner/v1/mother-accounts/"+createdMother.Id.String(), bytes.NewReader(motherUpdateBody))
	motherUpdateReq.SetPathValue("accountId", createdMother.Id.String())
	motherUpdateReq.Header.Set("Origin", "https://owner.test")
	motherUpdateReq.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
	motherUpdateReq.Header.Set(auth.CSRFHeaderName, csrf)
	motherUpdateReq.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: rotated})
	motherUpdateRec := httptest.NewRecorder()
	handler.updateMotherAccount(motherUpdateRec, motherUpdateReq, ownerapi.UpdateMotherAccountParams{IfMatch: `"1"`})
	if motherUpdateRec.Code != 200 {
		t.Fatalf("updateMotherAccount 16384 password=%d %s", motherUpdateRec.Code, motherUpdateRec.Body.String())
	}

	if _, err = pool.Exec(ctx, `UPDATE tsw_target_credentials SET password_secret='tampered',secret_revision=secret_revision+1,version=version+1 WHERE target_account_id=$1`, waitingID); err != nil {
		t.Fatal(err)
	}
	if err = sealExistingTargetMaterials(ctx, pool, ring); err == nil {
		t.Fatal("startup accepted tampered sealed material")
	}
}
