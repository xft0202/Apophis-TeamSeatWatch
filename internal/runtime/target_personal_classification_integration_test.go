//go:build integration

package runtime

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/task"
)

func TestSavedTargetPersonalProbePersistsRealAdapterClassifications(t *testing.T) {
	pool, h, _, sessionToken, csrf := childReviewFixture(t)
	ctx := context.Background()
	if err := sealExistingTargetMaterials(ctx, pool, cardIntegrationKeyRing{}); err != nil {
		t.Fatal(err)
	}
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM tsw_target_accounts WHERE identifier='target@example.com'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	h.personalRefresh = &targetRefreshFixture{session: platform.PersonalSession{AccessToken: "personal-at", DeviceID: "device", Cookies: []platform.SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "cookie"}}, ExpiresAt: time.Now().Add(time.Hour)}}
	req := httptest.NewRequest("POST", "/api/owner/v1/target-accounts/"+id.String()+"/personal-session", nil)
	req.Header.Set("Origin", "https://owner.test")
	req.Header.Set(auth.CSRFHeaderName, csrf)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionToken})
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
	rec := httptest.NewRecorder()
	h.RefreshTargetPersonalAccess(rec, req, id, ownerapi.RefreshTargetPersonalAccessParams{})
	if rec.Code != 200 {
		t.Fatalf("refresh: %d %s", rec.Code, rec.Body.String())
	}
	sessionToken = childRotatedSession(t, rec)
	cases := []struct {
		name              string
		status            int
		contentType, body string
		network           bool
		want              string
		verified          bool
	}{
		{"success", 200, "application/json", `{"rate_limit":{"primary_window":{"used_percent":10}}}`, false, "available", true},
		{"401", 401, "text/plain", "expired", false, "credential_invalid", false},
		{"403", 403, "application/json", `{"error":{"code":"forbidden"}}`, false, "forbidden", false},
		{"deactivated", 403, "application/json", `{"error":{"code":"account_deactivated"}}`, false, "banned", true},
		{"network", 0, "", "", true, "network_error", false},
		{"malformed", 200, "application/json", `{}`, false, "unknown", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			preview := personalPreview(t, h, sessionToken, csrf, map[string]any{"targetAccountIds": []string{id.String()}})
			batch := decodePersonalBatch(t, personalRequest(t, h, sessionToken, csrf, "POST", "/create", map[string]any{"targetAccountIds": []string{id.String()}, "expectedCount": 1, "confirmed": true, "requestKey": uuid.NewString(), "scopeToken": preview.ScopeToken}), 202)
			calls, releases := 0, 0
			probe := SavedTargetPersonalProbe{Pool: pool, KeyRing: cardIntegrationKeyRing{}, Adapter: platform.PersonalUsageProbe{Client: func(context.Context) (*http.Client, func(), error) {
				return &http.Client{Timeout: time.Second, Transport: usageFixtureTransport(func(req *http.Request) (*http.Response, error) {
					calls++
					if req.Method != "GET" || req.URL.String() != "https://chatgpt.com/backend-api/wham/usage" || req.Header.Get("Authorization") != "Bearer personal-at" || req.Header.Get("Cookie") != "" {
						t.Fatal("unsafe usage request")
					}
					if tc.network {
						return nil, errors.New("mock dial failure")
					}
					return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": []string{tc.contentType}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
				})}, func() { releases++ }, nil
			}}}
			worked, err := task.NewStore(pool, cardIntegrationKeyRing{}).ProcessPersonalProbes(ctx, probe)
			if !worked || err != nil {
				t.Fatalf("processing %t %v", worked, err)
			}
			result := decodePersonalBatch(t, personalRequest(t, h, sessionToken, csrf, "GET", "/"+batch.Id.String(), nil), 200)
			item := result.Items[0]
			if item.Outcome == nil || string(*item.Outcome) != tc.want || item.VerifiedEvidence != tc.verified || calls != 1 || releases != 1 {
				t.Fatalf("persisted %+v calls %d releases %d", item, calls, releases)
			}
		})
	}
}
