//go:build integration

package runtime

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/task"
)

func TestPersonalProbeCancelWhileProviderRunningFencesLateSuccess(t *testing.T) {
	pool, h, _, session, csrf := childReviewFixture(t)
	ctx := context.Background()
	var id string
	err := pool.QueryRow(ctx, `INSERT INTO tsw_target_accounts(identifier,identifier_hmac,identifier_key_version,display_label)
 VALUES('cancel@test.invalid',decode(repeat('99',32),'hex'),1,'cancel') RETURNING id`).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	batch := decodePersonalBatch(t, personalRequest(t, h, session, csrf, "POST", "/create", map[string]any{
		"targetAccountIds": []string{id}, "expectedCount": 1, "confirmed": true, "requestKey": uuid.NewString(),
	}), 202)
	provider := personalFixtureProvider{responses: map[string]platform.PersonalProbeEvidence{id: {HTTPStatus: 200, VerifiedUsage: true}}}
	provider.onProbe = func(string) {
		canceled := decodePersonalBatch(t, personalRequest(t, h, session, csrf, "DELETE", "/"+batch.Id.String(), nil), 200)
		if canceled.Canceled != 1 || canceled.Running != 0 {
			t.Fatalf("running attempt not canceled: %+v", canceled)
		}
	}
	worked, err := task.NewStore(pool, cardIntegrationKeyRing{}).ProcessPersonalProbes(ctx, provider)
	if !worked || err != nil {
		t.Fatalf("cancel processing: %t %v", worked, err)
	}
	current := decodePersonalBatch(t, personalRequest(t, h, session, csrf, "GET", "/"+batch.Id.String(), nil), 200)
	if current.Canceled != 1 || current.Succeeded != 0 || current.Items[0].Outcome != nil {
		t.Fatalf("late success certified after cancel: %+v", current)
	}
}
