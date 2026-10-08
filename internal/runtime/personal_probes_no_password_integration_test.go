//go:build integration

package runtime

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/task"
)

func TestPersonalProbeSavedPasswordNeverEnablesBasicAuth(t *testing.T) {
	pool, h, _, session, csrf := childReviewFixture(t)
	ctx := context.Background()
	var targetID string
	if err := pool.QueryRow(ctx, `SELECT id FROM tsw_target_accounts WHERE identifier='target@example.com'`).Scan(&targetID); err != nil {
		t.Fatal(err)
	}
	// The canonical graph account has sealed password/TOTP, but no Personal session.
	preview := personalPreview(t, h, session, csrf, map[string]any{"targetAccountIds": []string{targetID}})
	batch := decodePersonalBatch(t, personalRequest(t, h, session, csrf, "POST", "/create", map[string]any{
		"targetAccountIds": []string{targetID}, "expectedCount": 1, "confirmed": true, "requestKey": uuid.NewString(), "scopeToken": preview.ScopeToken,
	}), 202)
	worker := &task.Worker{Store: task.NewStore(pool, cardIntegrationKeyRing{}), PersonalProber: task.MissingPersonalProvider{}}
	// No platform client/egress/reader is supplied: a password-backed call would fail.
	worked, err := worker.RunOnce(ctx)
	if !worked || err != nil {
		t.Fatalf("Personal worker attempted old platform path: %t %v", worked, err)
	}
	result := decodePersonalBatch(t, personalRequest(t, h, session, csrf, "GET", "/"+batch.Id.String(), nil), 200)
	if result.Failed != 1 || result.Succeeded != 0 || result.Items[0].Outcome == nil || *result.Items[0].Outcome != "missing_personal_credential" || result.Items[0].HttpStatus != nil {
		t.Fatalf("saved password treated as Personal credential: %+v", result)
	}
	var legacyStatus *string
	if err = pool.QueryRow(ctx, `SELECT latest_probe_status FROM tsw_target_credentials WHERE target_account_id=$1`, targetID).Scan(&legacyStatus); err != nil || legacyStatus != nil {
		t.Fatalf("legacy BasicAuth projection mutated: %v %v", legacyStatus, err)
	}
}
