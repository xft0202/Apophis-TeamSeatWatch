//go:build integration

package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/task"
)

type personalFixtureProvider struct {
	responses map[string]platform.PersonalProbeEvidence
	fail      error
	onProbe   func(string)
}

func (p personalFixtureProvider) ProbePersonal(_ context.Context, id string) (platform.PersonalProbeEvidence, error) {
	if p.onProbe != nil {
		p.onProbe(id)
	}
	return p.responses[id], p.fail
}

func personalRequest(t *testing.T, h *OwnerAuthHandler, session, csrf, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	req.Header.Set("Origin", "https://owner.test")
	req.Header.Set(auth.CSRFHeaderName, csrf)
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
	recorder := httptest.NewRecorder()
	switch {
	case method == "POST" && path == "/preview":
		h.PreviewPersonalProbes(recorder, req, ownerapi.PreviewPersonalProbesParams{})
	case method == "POST":
		h.CreatePersonalProbes(recorder, req, ownerapi.CreatePersonalProbesParams{})
	case method == "DELETE":
		h.CancelPersonalProbes(recorder, req, uuid.MustParse(path[1:]), ownerapi.CancelPersonalProbesParams{})
	case strings.HasPrefix(path, "/request/"):
		h.GetPersonalProbesByRequest(recorder, req, uuid.MustParse(strings.TrimPrefix(path, "/request/")))
	default:
		h.GetPersonalProbes(recorder, req, uuid.MustParse(path[1:]))
	}
	return recorder
}
func personalPreview(t *testing.T, h *OwnerAuthHandler, session, csrf string, scope any) ownerapi.PersonalProbePreview {
	t.Helper()
	response := personalRequest(t, h, session, csrf, "POST", "/preview", scope)
	if response.Code != 200 {
		t.Fatalf("preview %d: %s", response.Code, response.Body.String())
	}
	var preview ownerapi.PersonalProbePreview
	if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.ScopeToken == "" {
		t.Fatal("preview omitted confirmed ID/version token")
	}
	return preview
}

func decodePersonalBatch(t *testing.T, rec *httptest.ResponseRecorder, status int) ownerapi.PersonalProbeBatch {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d != %d: %s", rec.Code, status, rec.Body.String())
	}
	var batch ownerapi.PersonalProbeBatch
	if err := json.Unmarshal(rec.Body.Bytes(), &batch); err != nil {
		t.Fatal(err)
	}
	return batch
}

func TestPersonalProbeScopeProgressSaveFailureAndCancel(t *testing.T) {
	pool, h, _, session, csrf := childReviewFixture(t)
	ctx := context.Background()
	ids := []string{}
	for i := 0; i < 45; i++ {
		var id string
		identifier := fmt.Sprintf("probe%02d@crosspage.test", i)
		err := pool.QueryRow(ctx, `INSERT INTO tsw_target_accounts(identifier,identifier_hmac,identifier_key_version,display_label)
   VALUES ($1,decode(md5($1)||md5($1||'salt'),'hex'),1,$1) RETURNING id`, identifier).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	selected := map[string]any{"targetAccountIds": []string{ids[0], ids[35]}, "search": "no-match"}
	preview := personalPreview(t, h, session, csrf, selected)
	if preview.Count != 2 {
		t.Fatalf("selected precedence: %+v", preview)
	}
	create := map[string]any{"targetAccountIds": []string{ids[0], ids[35]}, "search": "no-match", "expectedCount": 2, "confirmed": true, "requestKey": uuid.NewString(), "scopeToken": preview.ScopeToken}
	batch := decodePersonalBatch(t, personalRequest(t, h, session, csrf, "POST", "/create", create), 202)
	if batch.Total != 2 || batch.Queued != 2 {
		t.Fatalf("selection ignored: %+v", batch)
	}
	recovered := decodePersonalBatch(t, personalRequest(t, h, session, csrf, "GET", "/request/"+create["requestKey"].(string), nil), 200)
	if recovered.Id != batch.Id {
		t.Fatalf("lost response not recoverable: %s vs %s", recovered.Id, batch.Id)
	}
	replayed := decodePersonalBatch(t, personalRequest(t, h, session, csrf, "POST", "/create", create), 202)
	if replayed.Id != batch.Id || replayed.Total != 2 {
		t.Fatalf("idempotency replay: %+v", replayed)
	}
	// Cancellation is durable and cannot report either skipped account as normal.
	canceled := decodePersonalBatch(t, personalRequest(t, h, session, csrf, "DELETE", "/"+batch.Id.String(), nil), 200)
	if canceled.Canceled != 2 || canceled.Succeeded != 0 {
		t.Fatalf("cancel: %+v", canceled)
	}
	store := task.NewStore(pool, cardIntegrationKeyRing{})
	if worked, err := store.ProcessPersonalProbes(ctx, personalFixtureProvider{}); err != nil || worked {
		t.Fatalf("canceled job processed: %t %v", worked, err)
	}
	filtered := map[string]any{"search": "crosspage.test"}
	preview = personalPreview(t, h, session, csrf, filtered)
	if preview.Count != 45 {
		t.Fatalf("cross-page preview: %+v", preview)
	}
	filtered["scopeToken"] = preview.ScopeToken
	filtered["expectedCount"] = 44
	filtered["confirmed"] = true
	filtered["requestKey"] = uuid.NewString()
	if got := personalRequest(t, h, session, csrf, "POST", "/create", filtered); got.Code != 409 {
		t.Fatalf("stale count accepted: %d", got.Code)
	}
	filtered["expectedCount"] = 45
	batch = decodePersonalBatch(t, personalRequest(t, h, session, csrf, "POST", "/create", filtered), 202)
	if batch.Total != 45 || len(batch.Items) != 45 {
		t.Fatalf("filtered page truncated: %+v", batch)
	}
	// A DB publication failure leaves a running item, never succeeded.
	_, err := pool.Exec(ctx, `CREATE FUNCTION fail_personal_save() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
  IF NEW.status='failed' THEN RAISE EXCEPTION 'disposable save failure'; END IF; RETURN NEW; END $$;
  CREATE TRIGGER fail_personal_save BEFORE UPDATE ON tsw_personal_probe_items FOR EACH ROW EXECUTE FUNCTION fail_personal_save()`)
	if err != nil {
		t.Fatal(err)
	}
	worked, err := store.ProcessPersonalProbes(ctx, task.MissingPersonalProvider{})
	if !worked || err == nil {
		t.Fatalf("save failure undetected: %t %v", worked, err)
	}
	batch = decodePersonalBatch(t, personalRequest(t, h, session, csrf, "GET", "/"+batch.Id.String(), nil), 200)
	if batch.Running != 1 || batch.Succeeded != 0 || batch.Failed != 0 {
		t.Fatalf("save failure certified: %+v", batch)
	}
	failedSave := 0
	for _, item := range batch.Items {
		if item.EvidenceCode != nil && *item.EvidenceCode == "result_persistence_failed" {
			failedSave++
			if item.Outcome != nil {
				t.Fatalf("unpersisted result projected: %+v", item)
			}
		}
	}
	if failedSave != 1 {
		t.Fatalf("persistence failure not distinct: %+v", batch)
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER fail_personal_save ON tsw_personal_probe_items;DROP FUNCTION fail_personal_save();UPDATE tsw_personal_probe_items SET started_at=now()-interval '6 minutes' WHERE status='running'`); err != nil {
		t.Fatal(err)
	}
	// queued rows are processed first, then the fenced stale attempt.
	for i := 0; i < 45; i++ {
		worked, err = store.ProcessPersonalProbes(ctx, task.MissingPersonalProvider{})
		if !worked || err != nil {
			t.Fatalf("attempt %d: %t %v", i, worked, err)
		}
	}
	batch = decodePersonalBatch(t, personalRequest(t, h, session, csrf, "GET", "/"+batch.Id.String(), nil), 200)
	if batch.Failed != 45 || batch.Succeeded != 0 || batch.NotSavedOrRetained != 0 {
		t.Fatalf("missing credentials misreported: %+v", batch)
	}
	for _, item := range batch.Items {
		if item.Outcome == nil || *item.Outcome != "missing_personal_credential" || item.Endpoint != nil || item.HttpStatus != nil {
			t.Fatalf("password request or wrong result: %+v", item)
		}
	}
	// A deleted account cascades its row, leaves the original denominator and siblings.
	if _, err = pool.Exec(ctx, `DELETE FROM tsw_target_accounts WHERE id=$1`, ids[1]); err != nil {
		t.Fatal(err)
	}
	batch = decodePersonalBatch(t, personalRequest(t, h, session, csrf, "GET", "/"+batch.Id.String(), nil), 200)
	if batch.NotSavedOrRetained != 1 || batch.Failed != 44 || batch.Total != 45 {
		t.Fatalf("retained rows miscounted: %+v", batch)
	}
}

func TestPersonalProbeMockClassificationAndDeletionDuringAttempt(t *testing.T) {
	pool, h, _, session, csrf := childReviewFixture(t)
	ctx := context.Background()
	ids := []string{}
	for i := 0; i < 6; i++ {
		identifier := fmt.Sprintf("mock%02d@test.invalid", i)
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO tsw_target_accounts(identifier,identifier_hmac,identifier_key_version,display_label)
   VALUES($1,decode(md5($1)||md5($1||'salt'),'hex'),1,$1) RETURNING id`, identifier).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	preview := personalPreview(t, h, session, csrf, map[string]any{"targetAccountIds": ids})
	request := map[string]any{"targetAccountIds": ids, "expectedCount": 6, "confirmed": true, "requestKey": uuid.NewString(), "scopeToken": preview.ScopeToken}
	batch := decodePersonalBatch(t, personalRequest(t, h, session, csrf, "POST", "/create", request), 202)
	provider := personalFixtureProvider{responses: map[string]platform.PersonalProbeEvidence{
		ids[0]: {HTTPStatus: 200, VerifiedUsage: true}, ids[1]: {HTTPStatus: 401}, ids[2]: {HTTPStatus: 403},
		ids[3]: {HTTPStatus: 403, ErrorCode: "account_deactivated", VerifiedDeactivation: true},
		ids[4]: {TransportError: errors.New("dial refused")}, ids[5]: {HTTPStatus: 200, Malformed: true},
	}}
	store := task.NewStore(pool, cardIntegrationKeyRing{})
	for i := 0; i < 6; i++ {
		worked, err := store.ProcessPersonalProbes(ctx, provider)
		if !worked || err != nil {
			t.Fatalf("mock probe %d: %t %v", i, worked, err)
		}
	}
	batch = decodePersonalBatch(t, personalRequest(t, h, session, csrf, "GET", "/"+batch.Id.String(), nil), 200)
	got := map[string]string{}
	for _, item := range batch.Items {
		got[item.TargetAccountId.String()] = string(*item.Outcome)
		if item.VerifiedEvidence != (item.TargetAccountId.String() == ids[0] || item.TargetAccountId.String() == ids[3]) {
			t.Fatalf("unverified or missing trace for %s: %+v", item.Identifier, item)
		}
		if item.TargetAccountId.String() == ids[3] && (item.EvidenceCode == nil || *item.EvidenceCode != "account_deactivated" || item.Endpoint == nil || *item.Endpoint != "personal_usage") {
			t.Fatalf("ban lacks explicit endpoint/code: %+v", item)
		}
	}
	expected := []string{"available", "credential_invalid", "forbidden", "banned", "network_error", "unknown"}
	for i, id := range ids {
		if got[id] != expected[i] {
			t.Fatalf("mock %s: %s != %s", id, got[id], expected[i])
		}
	}
	if batch.Succeeded != 1 || batch.Failed != 5 {
		t.Fatalf("partial failure lost: %+v", batch)
	}
	if _, err := pool.Exec(ctx, `UPDATE tsw_personal_probe_items SET outcome='banned' WHERE batch_id=$1 AND target_account_id=$2`, batch.Id, ids[2]); err == nil {
		t.Fatal("ordinary 403 bypassed verified ban evidence constraint")
	}
	secondPreview := personalPreview(t, h, session, csrf, map[string]any{"targetAccountIds": []string{ids[4]}})
	second := decodePersonalBatch(t, personalRequest(t, h, session, csrf, "POST", "/create", map[string]any{"targetAccountIds": []string{ids[4]}, "expectedCount": 1, "confirmed": true, "requestKey": uuid.NewString(), "scopeToken": secondPreview.ScopeToken}), 202)
	provider.onProbe = func(id string) {
		if _, err := pool.Exec(ctx, `DELETE FROM tsw_target_accounts WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
	}
	worked, err := store.ProcessPersonalProbes(ctx, provider)
	if !worked || err != nil {
		t.Fatalf("late result failure: %t %v", worked, err)
	}
	second = decodePersonalBatch(t, personalRequest(t, h, session, csrf, "GET", "/"+second.Id.String(), nil), 200)
	if second.NotSavedOrRetained != 1 || second.Succeeded != 0 || len(second.Items) != 0 {
		t.Fatalf("deleted account resurrected: %+v", second)
	}
}
