//go:build integration

package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

type rotatedCardManagementRing struct{}

func (rotatedCardManagementRing) Current() (uint16, [32]byte) { return 8, [32]byte{1} }
func (rotatedCardManagementRing) Lookup(version uint16) ([32]byte, bool) {
	if version == 7 {
		return [32]byte{}, true
	}
	return [32]byte{1}, version == 8
}

func seedCardListMember(t *testing.T, f cardRevocationFixture, n int, activate bool) string {
	t.Helper()
	ctx := context.Background()
	membership, target, opTarget, asset, version := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	identifier := fmt.Sprintf("account%03d@example.test", n)
	hash := sha256.Sum256([]byte(identifier))
	queries := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES ($1,$2,$3,1,$2)`, []any{target, identifier, hash[:]}},
		{`INSERT INTO tsw_target_credentials(target_account_id,password_secret,totp_secret,material_status) SELECT $1,c.password_secret,c.totp_secret,c.material_status FROM tsw_target_credentials c JOIN tsw_batch_memberships m ON m.target_account_id=c.target_account_id WHERE m.id=$2`, []any{target, f.ids.membership}},
		{`INSERT INTO tsw_operation_targets(id,operation_id,target_account_id,ordinal) SELECT $1,ot.operation_id,$2,$3 FROM tsw_operation_targets ot JOIN tsw_batch_memberships m ON m.join_operation_target_id=ot.id WHERE m.id=$4`, []any{opTarget, target, n + 1, f.ids.membership}},
		{`INSERT INTO tsw_batch_memberships(id,batch_id,target_account_id,join_operation_target_id,joined_at) SELECT $1,batch_id,$2,$3,now() FROM tsw_batch_memberships WHERE id=$4`, []any{membership, target, opTarget, f.ids.membership}},
		{`UPDATE tsw_operation_targets SET target_account_id=NULL,membership_id=$2,status='succeeded',completed_at=now() WHERE id=$1`, []any{opTarget, membership}},
		{`INSERT INTO tsw_oauth_assets(id,membership_id,status,current_generation,platform_subject_id) VALUES ($1,$2,'ready',1,'subject')`, []any{asset, membership}},
		{`INSERT INTO tsw_delivery_versions(id,oauth_asset_id,generation,payload,payload_sha256,validated_platform_subject_id,validated_workspace_id) VALUES ($1,$2,1,'{}',decode(repeat('00',32),'hex'),'subject',$3)`, []any{version, asset, f.ids.workspace}},
		{`UPDATE tsw_oauth_assets SET current_delivery_version_id=$2 WHERE id=$1`, []any{asset, version}},
	}
	for _, q := range queries {
		if _, err := f.pool.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	if activate {
		secret := "TSW1-" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{byte(n)}, 20))
		response := activateCardIntegrationRequest(t, f.owner, membership.String(), f.sessionToken, f.csrfToken, secret, fmt.Sprintf("list-member-%03d", n))
		if response.Code != 201 {
			t.Fatalf("activation: %d %s", response.Code, response.Body.String())
		}
	}
	return membership.String()
}
func cardOwnerRequest(f cardRevocationFixture, method, path string, body any, csrf bool) *http.Request {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: f.sessionToken})
	r.Header.Set("Origin", "https://owner.test")
	if csrf {
		r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: f.csrfToken})
		r.Header.Set(auth.CSRFHeaderName, f.csrfToken)
	}
	return r
}
func TestCardManagementIntegration(t *testing.T) {
	f := newCardRevocationFixture(t)
	ctx := context.Background()
	var mother, batch uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT binding.mother_account_id,batch.id FROM tsw_batch_memberships m JOIN tsw_batches batch ON batch.id=m.batch_id JOIN tsw_mother_workspace_bindings binding ON binding.id=batch.binding_id WHERE m.id=$1`, f.ids.membership).Scan(&mother, &batch); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE tsw_batches SET source_batch_name='秋季账号批次' WHERE id=$1`, batch); err != nil {
		t.Fatal(err)
	}
	members := []string{f.ids.membership}
	for n := 1; n < 25; n++ {
		members = append(members, seedCardListMember(t, f, n, true))
	}
	seedCardListMember(t, f, 25, false)
	other, _ := seedOtherWorkspaceDelivery(t, f)
	// Freeze realistic terminal facts before asserting the computed state and its filters.
	claimed := publicRedeemRequest(t, f.public, http.MethodPost, "/api/public/v1/redeem/confirm", map[string]any{"cardSecret": f.secret}, nil)
	if claimed.Code != 200 {
		t.Fatalf("claim %d %s", claimed.Code, claimed.Body.String())
	}
	if r := revokeCardIntegrationRequest(t, f.owner, members[1], f.sessionToken, f.csrfToken, true, true); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	for _, q := range []struct {
		sql string
		id  string
	}{
		{`UPDATE tsw_cards SET issued_at=now()-interval '2 days',redemption_deadline=now()-interval '1 day' WHERE membership_id=$1`, members[2]},
		{`UPDATE tsw_batch_memberships SET state='removed',removed_at=now() WHERE id=$1`, members[3]},
		{`UPDATE tsw_cards SET sealed_secret=NULL WHERE membership_id=$1`, members[4]},
	} {
		if _, err := f.pool.Exec(ctx, q.sql, q.id); err != nil {
			t.Fatal(err)
		}
	}
	only := true
	workspace := uuid.MustParse(f.ids.workspace)
	params := ownerapi.ListDeliveryRecordsParams{CardsOnly: &only, MotherAccountId: &mother, WorkspaceId: &workspace}
	list := listDeliveryRecordsIntegrationRequest(t, f, params)
	if list.Total != 25 || len(list.Items) != 20 || list.PageSize != 20 {
		t.Fatalf("page total=%d items=%d size=%d", list.Total, len(list.Items), list.PageSize)
	}
	encoded, _ := json.Marshal(list)
	if bytes.Contains(encoded, []byte(f.secret)) || bytes.Contains(encoded, []byte("sealed_secret")) {
		t.Fatal("list exposed secret")
	}
	page := 2
	params.Page = &page
	second := listDeliveryRecordsIntegrationRequest(t, f, params)
	if len(second.Items) != 5 || second.Total != 25 {
		t.Fatal("second page/count mismatch")
	}
	size := 100
	params.Page = nil
	params.PageSize = &size
	all := listDeliveryRecordsIntegrationRequest(t, f, params)
	if len(all.Items) != 25 {
		t.Fatal("100-row pagination did not return bounded scope")
	}
	for _, item := range all.Items {
		if item.SourceBatchName == nil || *item.SourceBatchName != "秋季账号批次" || item.MotherAccountId != mother || item.WorkspaceId != workspace {
			t.Fatal("wrong provenance or scope")
		}
	}
	for _, state := range []ownerapi.CardState{ownerapi.CardStateClaimed, ownerapi.CardStateRevoked, ownerapi.CardStateExpired, ownerapi.CardStateUnavailable, ownerapi.CardStateUnclaimed} {
		params.CardState = &state
		value := listDeliveryRecordsIntegrationRequest(t, f, params)
		expected := 1
		if state == ownerapi.CardStateUnclaimed {
			expected = 21
		}
		if value.Total != expected {
			t.Fatalf("%s total=%d want=%d", state, value.Total, expected)
		}
		for _, item := range value.Items {
			if item.CardState != state {
				t.Fatal("status filter/projection disagree")
			}
		}
	}
	params.CardState = nil
	search := "秋季账号批次"
	params.Search = &search
	if got := listDeliveryRecordsIntegrationRequest(t, f, params); got.Total != 25 {
		t.Fatal("source batch search failed")
	}
	params.Search = nil
	wrong := uuid.New()
	params.MotherAccountId = &wrong
	if got := listDeliveryRecordsIntegrationRequest(t, f, params); got.Total != 0 {
		t.Fatal("mother filter leaked data")
	}
	params.MotherAccountId = &mother
	otherSpace := uuid.MustParse(other.workspace)
	params.WorkspaceId = &otherSpace
	if got := listDeliveryRecordsIntegrationRequest(t, f, params); got.Total != 1 {
		t.Fatal("workspace scope failed")
	}
	params.WorkspaceId = &workspace
	selectedResponse := httptest.NewRecorder()
	f.owner.SelectDeliveryCards(selectedResponse, cardOwnerRequest(f, "GET", "/api/owner/v1/deliveries/card-selection", nil, false), ownerapi.SelectDeliveryCardsParams{MotherAccountId: &mother, WorkspaceId: &workspace})
	var selected ownerapi.CardSelection
	if selectedResponse.Code != 200 || json.Unmarshal(selectedResponse.Body.Bytes(), &selected) != nil || selected.Total != 25 {
		t.Fatalf("selection %d %s", selectedResponse.Code, selectedResponse.Body.String())
	}
	exportBody := ownerapi.CardExportRequest{MotherAccountId: mother, WorkspaceId: workspace, Items: selected.Items}
	export := func(body ownerapi.CardExportRequest, csrf bool) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		f.owner.ExportDeliveryCards(r, cardOwnerRequest(f, "POST", "/api/owner/v1/deliveries/card-export", body, csrf), ownerapi.ExportDeliveryCardsParams{})
		return r
	}
	exported := export(exportBody, true)
	var exportedData ownerapi.CardExportResponse
	if exported.Code != 200 || json.Unmarshal(exported.Body.Bytes(), &exportedData) != nil || len(exportedData.Items) != 24 || len(exportedData.Unavailable) != 1 || exported.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("export %d %s", exported.Code, exported.Body.String())
	}
	if r := export(exportBody, false); r.Code != 403 {
		t.Fatalf("CSRF accepted %d", r.Code)
	}
	exportBody.MotherAccountId = wrong
	if r := export(exportBody, true); r.Code != 409 {
		t.Fatal("export escaped mother scope")
	}
	exportBody.MotherAccountId = mother
	exportBody.Items = []ownerapi.CardSelectionEntry{selected.Items[0], selected.Items[0]}
	if r := export(exportBody, true); r.Code != 422 {
		t.Fatal("duplicate export accepted")
	}
	exportBody.Items = selected.Items
	copyRecord := func(id string, version int64, loggedIn bool) *httptest.ResponseRecorder {
		r := cardOwnerRequest(f, "GET", "/api/owner/v1/deliveries/"+id+"/card-secret", nil, false)
		if !loggedIn {
			r.Header.Del("Cookie")
		}
		w := httptest.NewRecorder()
		f.owner.GetDeliveryCardSecret(w, r, uuid.MustParse(id), ownerapi.GetDeliveryCardSecretParams{CardVersion: version})
		return w
	}
	first := copyRecord(f.ids.membership, 1, true)
	if first.Code != 200 || !bytes.Contains(first.Body.Bytes(), []byte(f.secret)) || first.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("stored secret not readable")
	}
	f.owner.keyRing = rotatedCardManagementRing{}
	if r := copyRecord(f.ids.membership, 1, true); r.Code != 200 {
		t.Fatal("stored card lost after encryption key rotation")
	}
	f.owner.keyRing = cardIntegrationKeyRing{}
	var sealed []byte
	if err := f.pool.QueryRow(ctx, `SELECT sealed_secret FROM tsw_cards WHERE membership_id=$1`, f.ids.membership).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte(f.secret)) {
		t.Fatal("secret stored in plaintext")
	}
	if r := copyRecord(members[4], 1, true); r.Code != 409 {
		t.Fatal("missing historical secret fabricated")
	}
	if r := copyRecord(f.ids.membership, 1, false); r.Code != 401 {
		t.Fatal("unauthenticated secret read accepted")
	}
	if r := revokeCardIntegrationRequest(t, f.owner, members[5], f.sessionToken, f.csrfToken, true, true); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if r := copyRecord(members[5], 1, true); r.Code != 409 {
		t.Fatal("stale card version copied")
	}
	if r := export(exportBody, true); r.Code != 409 {
		t.Fatal("stale frozen export accepted")
	}
	detail := httptest.NewRecorder()
	f.owner.GetDeliveryRecord(detail, cardOwnerRequest(f, "GET", "/api/owner/v1/deliveries/"+f.ids.membership, nil, false), uuid.MustParse(f.ids.membership))
	if detail.Code != 200 || !bytes.Contains(detail.Body.Bytes(), []byte("card.activated")) {
		t.Fatalf("activation missing in timeline %s", detail.Body.String())
	}
}
