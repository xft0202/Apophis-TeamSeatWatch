//go:build integration

package runtime

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// A count-only confirmation used to allow a different account when the filter
// membership changed without changing its cardinality.
func TestPersonalProbeRejectsSameCountScopeReplacementAndVersionChange(t *testing.T) {
	pool, h, _, session, csrf := childReviewFixture(t)
	ctx := context.Background()
	var oldID, newID string
	for _, item := range []struct {
		identifier, label string
		id                *string
	}{
		{"former@scope.test", "tracked-scope", &oldID},
		{"replacement@scope.test", "other", &newID},
	} {
		err := pool.QueryRow(ctx, `INSERT INTO tsw_target_accounts(identifier,identifier_hmac,identifier_key_version,display_label)
   VALUES($1,decode(md5($1)||md5($1||'salt'),'hex'),1,$2) RETURNING id`, item.identifier, item.label).Scan(item.id)
		if err != nil {
			t.Fatal(err)
		}
	}
	scope := map[string]any{"search": "tracked-scope"}
	preview := personalPreview(t, h, session, csrf, scope)
	if preview.Count != 1 {
		t.Fatalf("wrong preview count: %+v", preview)
	}
	if _, err := pool.Exec(ctx, `UPDATE tsw_target_accounts SET display_label='other',version=version+1 WHERE id=$1`, oldID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tsw_target_accounts SET display_label='tracked-scope',version=version+1 WHERE id=$1`, newID); err != nil {
		t.Fatal(err)
	}
	stale := map[string]any{"search": "tracked-scope", "expectedCount": 1, "confirmed": true, "requestKey": uuid.NewString(), "scopeToken": preview.ScopeToken}
	if got := personalRequest(t, h, session, csrf, "POST", "/create", stale); got.Code != 409 {
		t.Fatalf("same-count replacement dispatched: %d %s", got.Code, got.Body.String())
	}
	fresh := personalPreview(t, h, session, csrf, scope)
	if fresh.Count != 1 || fresh.ScopeToken == preview.ScopeToken {
		t.Fatalf("replacement retained preview token: %+v", fresh)
	}
	stale["scopeToken"] = fresh.ScopeToken
	if _, err := pool.Exec(ctx, `UPDATE tsw_target_accounts SET version=version+1 WHERE id=$1`, newID); err != nil {
		t.Fatal(err)
	}
	if got := personalRequest(t, h, session, csrf, "POST", "/create", stale); got.Code != 409 {
		t.Fatalf("version change dispatched: %d %s", got.Code, got.Body.String())
	}
	final := personalPreview(t, h, session, csrf, scope)
	stale["scopeToken"] = final.ScopeToken
	batch := decodePersonalBatch(t, personalRequest(t, h, session, csrf, "POST", "/create", stale), 202)
	if batch.Total != 1 || batch.Items[0].TargetAccountId.String() != newID {
		t.Fatalf("confirmed account mismatch: %+v", batch)
	}
}
