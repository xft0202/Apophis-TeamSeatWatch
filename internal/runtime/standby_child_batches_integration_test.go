//go:build integration

package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

func standbyRequest(t *testing.T, h *OwnerAuthHandler, session, csrf, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	req.Header.Set("Origin", "https://owner.test")
	req.Header.Set(auth.CSRFHeaderName, csrf)
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
	if session != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
	}
	result := httptest.NewRecorder()
	switch {
	case strings.HasSuffix(path, "/selection"):
		h.PreviewStandbyChildSelection(result, req, ownerapi.PreviewStandbyChildSelectionParams{})
	case strings.HasSuffix(path, "/export"):
		h.ExportStandbyChildBatch(result, req, uuid.MustParse(strings.Split(path, "/")[5]), ownerapi.ExportStandbyChildBatchParams{})
	case method == "GET":
		h.ListStandbyChildBatches(result, req)
	case method == "PATCH":
		h.UpdateStandbyChildBatch(result, req, uuid.MustParse(strings.Split(path, "/")[5]), ownerapi.UpdateStandbyChildBatchParams{})
	default:
		h.CreateStandbyChildBatch(result, req, ownerapi.CreateStandbyChildBatchParams{})
	}
	return result
}
func decodeStandby[T any](t *testing.T, r *httptest.ResponseRecorder, code int) T {
	t.Helper()
	if r.Code != code {
		t.Fatalf("status=%d expected=%d body=%s", r.Code, code, r.Body.String())
	}
	var value T
	if err := json.Unmarshal(r.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestStandbyChildBatchFrozenTransfersAndTXTIntegration(t *testing.T) {
	pool, h, _, session, csrf := childReviewFixture(t)
	ctx := context.Background()
	if err := sealExistingTargetMaterials(ctx, pool, h.keyRing); err != nil {
		t.Fatal(err)
	}
	accountIDs := make([]uuid.UUID, 0, 3)
	for _, address := range []string{"first@alpha.test", "second@alpha.test", "third@beta.test"} {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		item, err := h.insertTargetAccountTx(httptest.NewRequest("POST", "/", nil), tx, address, address, "password-"+address, "JBSWY3DPEHPK3PXP", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		accountIDs = append(accountIDs, item.Id)
	}
	path := "/api/owner/v1/standby-child-batches"
	preview := func(scope string, ids []uuid.UUID, search string, batch *uuid.UUID) ownerapi.StandbyChildSelection {
		var body any
		switch scope {
		case "selected":
			body = map[string]any{"scope": scope, "accountIds": ids}
		case "batch":
			body = map[string]any{"scope": scope, "batchId": batch}
		default:
			body = map[string]any{"scope": scope, "search": search}
		}
		return decodeStandby[ownerapi.StandbyChildSelection](t, standbyRequest(t, h, session, csrf, "POST", path+"/selection", body), 200)
	}
	save := func(name string, selection ownerapi.StandbyChildSelection, batch *ownerapi.StandbyChildBatch, action string) ownerapi.StandbyChildBatch {
		body := map[string]any{"name": name, "selection": selection, "expectedCount": selection.Count, "confirmed": true}
		method, endpoint, code := "POST", path, 201
		if batch != nil {
			method, endpoint, code = "PATCH", path+"/"+batch.Id.String(), 200
			body["expectedVersion"] = batch.Version
			body["action"] = action
		}
		return decodeStandby[ownerapi.StandbyChildBatch](t, standbyRequest(t, h, session, csrf, method, endpoint, body), code)
	}
	selected := preview("selected", accountIDs[:2], "", nil)
	if selected.Count != 2 {
		t.Fatalf("selected count=%d", selected.Count)
	}
	first := save("First", selected, nil, "")
	if first.MemberCount != 2 || first.DomainCount != 1 || len(first.Domains) != 1 || first.Domains[0] != "alpha.test" {
		t.Fatalf("distinct members/domains: %+v", first)
	}
	first = save("First", preview("batch", nil, "", &first.Id), &first, "add")
	if first.MemberCount != 2 || first.Version != 1 {
		t.Fatalf("same batch duplicated: %+v", first)
	}
	other := save("Second", preview("selected", accountIDs[1:], "", nil), nil, "")
	if other.MemberCount != 2 || other.DomainCount != 2 {
		t.Fatalf("transfer counts %+v", other)
	}
	listing := decodeStandby[[]ownerapi.StandbyChildBatch](t, standbyRequest(t, h, session, csrf, "GET", path, nil), 200)
	if len(listing) != 2 || strings.Contains(string(mustJSON(t, listing)), "password-") {
		t.Fatalf("list exposed secret or lost batch: %+v", listing)
	}
	stale := standbyRequest(t, h, session, csrf, "PATCH", path+"/"+first.Id.String(), map[string]any{"name": "First", "selection": selected, "expectedCount": 2, "confirmed": true, "expectedVersion": first.Version, "action": "add"})
	if stale.Code != 409 {
		t.Fatalf("stale selection=%d: %s", stale.Code, stale.Body.String())
	}
	// A filtered preview freezes only its returned IDs: a newly imported account cannot widen it.
	filtered := preview("filtered", nil, "@alpha.test", nil)
	if filtered.Count != 2 {
		t.Fatalf("filter count=%d", filtered.Count)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.insertTargetAccountTx(httptest.NewRequest("POST", "/", nil), tx, "later@alpha.test", "later@alpha.test", "pw", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	third := save("Frozen", filtered, nil, "")
	if third.MemberCount != 2 {
		t.Fatalf("filtered preview widened: %+v", third)
	}
	snapshot := preview("batch", nil, "", &third.Id)
	exported := standbyRequest(t, h, session, csrf, "POST", path+"/"+third.Id.String()+"/export", map[string]any{"selection": snapshot, "expectedCount": 2, "expectedVersion": third.Version, "confirmed": true})
	if exported.Code != 200 || strings.Count(exported.Body.String(), "\n") != 2 || strings.Contains(exported.Body.String(), "oauth") || strings.Contains(exported.Body.String(), "later@") {
		t.Fatalf("unsafe TXT: %d %s", exported.Code, exported.Body.String())
	}
	for _, line := range strings.Split(strings.TrimSpace(exported.Body.String()), "\n") {
		fields := strings.Split(line, "----")
		if len(fields) != 3 || !strings.Contains(fields[0], "@alpha.test") || !strings.HasPrefix(fields[1], "password-") || fields[2] != "JBSWY3DPEHPK3PXP" {
			t.Fatal("TXT must contain only account, password and 2FA")
		}
	}
	// Competing transfers of the same frozen account: exactly one may succeed.
	listing = decodeStandby[[]ownerapi.StandbyChildBatch](t, standbyRequest(t, h, session, csrf, "GET", path, nil), 200)
	for _, current := range listing {
		if current.Id == first.Id {
			first = current
		}
		if current.Id == other.Id {
			other = current
		}
	}
	one := preview("selected", accountIDs[:1], "", nil)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, b := range []ownerapi.StandbyChildBatch{first, other} {
		wg.Add(1)
		go func(b ownerapi.StandbyChildBatch) {
			defer wg.Done()
			result := standbyRequest(t, h, session, csrf, "PATCH", path+"/"+b.Id.String(), map[string]any{"name": b.Name, "selection": one, "expectedCount": 1, "confirmed": true, "expectedVersion": b.Version, "action": "add"})
			codes <- result.Code
		}(b)
	}
	wg.Wait()
	close(codes)
	success, conflict := 0, 0
	for code := range codes {
		if code == 200 {
			success++
		}
		if code == 409 {
			conflict++
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("concurrent transfers: successes=%d conflicts=%d", success, conflict)
	}
	staleExport := standbyRequest(t, h, session, csrf, "POST", path+"/"+third.Id.String()+"/export", map[string]any{"selection": snapshot, "expectedCount": 2, "expectedVersion": third.Version, "confirmed": true})
	if staleExport.Code != 409 {
		t.Fatalf("moved batch export=%d", staleExport.Code)
	}
	var history int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM tsw_standby_child_history WHERE target_account_id=$1`, accountIDs[0]).Scan(&history); err != nil || history < 2 {
		t.Fatalf("history=%d err=%v", history, err)
	}
	// A preexisting canonical child already has an operational workspace membership
	// and OAuth asset. Standby grouping must not change either record.
	var operationalID uuid.UUID
	if err = pool.QueryRow(ctx, `SELECT id FROM tsw_target_accounts WHERE identifier='target@example.com'`).Scan(&operationalID); err != nil {
		t.Fatal(err)
	}
	grouped := save("Workspace-independent", preview("selected", []uuid.UUID{operationalID}, "", nil), nil, "")
	if grouped.MemberCount != 1 {
		t.Fatalf("workspace child counted more than once: %+v", grouped)
	}
	var memberships, assets int
	if err = pool.QueryRow(ctx, `SELECT count(*), count(o.id) FROM tsw_batch_memberships m JOIN tsw_oauth_assets o ON o.membership_id=m.id WHERE m.target_account_id=$1`, operationalID).Scan(&memberships, &assets); err != nil || memberships != 1 || assets != 1 {
		t.Fatalf("operational history changed: memberships=%d assets=%d err=%v", memberships, assets, err)
	}
	missing := standbyRequest(t, h, "", csrf, "POST", path+"/selection", map[string]any{"scope": "selected", "accountIds": accountIDs[:1]})
	if missing.Code != 401 {
		t.Fatalf("session=%d", missing.Code)
	}
	noCSRF := standbyRequest(t, h, session, "wrong", "POST", path+"/selection", map[string]any{"scope": "selected", "accountIds": accountIDs[:1]})
	if noCSRF.Code != 403 {
		t.Fatalf("csrf=%d", noCSRF.Code)
	}
	badOrigin := httptest.NewRequest("POST", path+"/selection", strings.NewReader(`{"scope":"selected"}`))
	badOrigin.Header.Set("Origin", "https://attacker.test")
	badOrigin.Header.Set(auth.CSRFHeaderName, csrf)
	badOrigin.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
	badOrigin.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
	blocked := httptest.NewRecorder()
	h.PreviewStandbyChildSelection(blocked, badOrigin, ownerapi.PreviewStandbyChildSelectionParams{})
	if blocked.Code != 403 {
		t.Fatalf("origin=%d", blocked.Code)
	}
}
func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
