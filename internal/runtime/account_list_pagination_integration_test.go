//go:build integration

package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

func TestAccountListPaginationAndSavedPersonalFilters(t *testing.T) {
	pool, h, owner, session, _ := childReviewFixture(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var first, stale string
	for i := 0; i < 45; i++ {
		identifier := fmt.Sprintf("account%02d@paging.test", i)
		item, err := h.insertTargetAccountTx(httptest.NewRequest("POST", "/", nil), tx, identifier, identifier, "test-password", "JBSWY3DPEHPK3PXP", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = item.Id.String()
		}
		if i == 1 {
			stale = item.Id.String()
		}
	}
	for _, identifier := range []string{"near@paging.test.extra", "sub@sub.paging.test"} {
		if _, err := h.insertTargetAccountTx(httptest.NewRequest("POST", "/", nil), tx, identifier, identifier, "test-password", "JBSWY3DPEHPK3PXP", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var batch string
	if err := pool.QueryRow(ctx, `INSERT INTO tsw_personal_probe_batches(owner_id,request_key,scope_hash,scope_key_version,confirmed_scope_token,scope,scope_label,total)
 VALUES ($1,'paging-test','fixture',1,'fixture','selected','fixture',2) RETURNING id`, owner).Scan(&batch); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO tsw_personal_probe_items(batch_id,target_account_id,identifier,status,outcome,verified_evidence,finished_at)
 SELECT $1,id,identifier,'succeeded','available',true,clock_timestamp() FROM tsw_target_accounts WHERE id=ANY($2::uuid[])`, batch, []string{first, stale}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tsw_target_accounts SET updated_at=clock_timestamp()+interval '1 second' WHERE id=$1`, stale); err != nil {
		t.Fatal(err)
	}
	list := func(query string) ownerapi.TargetAccountList {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/owner/v1/target-accounts?sort=identifier_asc&"+query, nil)
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
		w := httptest.NewRecorder()
		ownerapi.Handler(h).ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("list status=%d: %s", w.Code, w.Body.String())
		}
		var result ownerapi.TargetAccountList
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	firstPage := list("domain=%40PAGING.TEST")
	if firstPage.Total != 45 || firstPage.PageSize != 20 || len(firstPage.Items) != 20 {
		t.Fatalf("first page total=%d size=%d items=%d", firstPage.Total, firstPage.PageSize, len(firstPage.Items))
	}
	for _, item := range firstPage.Items {
		if item.TokenStatus == nil || item.TokenStatus.HasAccessToken || item.TokenStatus.HasRefreshToken {
			t.Fatalf("new account token presence: %+v", item)
		}
	}
	secondPage := list("domain=paging.test&page=2&page_size=20")
	if secondPage.Total != 45 || len(secondPage.Items) != 20 || secondPage.Items[0].Identifier != "account20@paging.test" {
		t.Fatalf("second page: %+v", secondPage)
	}
	lastPage := list("domain=paging.test&page=3")
	if len(lastPage.Items) != 5 || lastPage.Total != 45 {
		t.Fatalf("last page: %+v", lastPage)
	}
	emptyPage := list("domain=paging.test&page=4")
	if len(emptyPage.Items) != 0 || emptyPage.Total != 45 {
		t.Fatalf("empty page lost total: %+v", emptyPage)
	}
	available := list("domain=paging.test&probe_status=available")
	if available.Total != 1 || len(available.Items) != 1 || available.Items[0].Id.String() != first || available.Items[0].LatestProbedAt == nil {
		t.Fatalf("saved Personal result filter: %+v", available)
	}
	unprobed := list("domain=paging.test&probe_status=unprobed")
	if unprobed.Total != 44 || len(unprobed.Items) != 20 {
		t.Fatalf("stale result filter: %+v", unprobed)
	}
	searched := list("domain=paging.test&search=account04")
	if searched.Total != 1 || len(searched.Items) != 1 {
		t.Fatalf("combined filters: %+v", searched)
	}

	id := uuid.MustParse(first)
	login, err := frontendPersonal{identifier: "account00@paging.test"}.RefreshPersonal(ctx, platform.MotherMaterial{})
	if err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err = pool.QueryRow(ctx, `SELECT secret_revision FROM tsw_target_credentials WHERE target_account_id=$1`, id).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	keyVersion, nonce, sealed, err := sealSessionFor("target", h.keyRing, id, revision, login.Session)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO tsw_target_personal_access(target_account_id,secret_revision,attempt,status,checked_at) VALUES ($1,$2,1,'ready',now())`, id, revision); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO tsw_target_personal_sessions(target_account_id,secret_revision,attempt,generation,key_version,nonce,sealed_session,expires_at) VALUES ($1,$2,1,$3,$4,$5,$6,$7)`, id, revision, uuid.New(), keyVersion, nonce, sealed, login.Session.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	saved := list("domain=paging.test&search=account00")
	if len(saved.Items) != 1 || saved.Items[0].PersonalAccess == nil || saved.Items[0].PersonalAccess.Status != "ready" || saved.Items[0].PersonalAccess.ExpiresAt == nil {
		t.Fatalf("saved login missing from page: %+v", saved)
	}
	if !saved.Items[0].TokenStatus.HasAccessToken || saved.Items[0].TokenStatus.HasRefreshToken {
		t.Fatalf("Personal login must save only AT: %+v", saved.Items[0].TokenStatus)
	}
	for filter, total := range map[string]int64{"has_at": 1, "missing_at": 44, "has_rt": 0, "missing_rt": 45, "both": 0, "none": 44} {
		result := list("domain=paging.test&token_status=" + filter)
		if result.Total != total || len(result.Items) != min(20, int(total)) {
			t.Fatalf("token filter %s: total=%d items=%d want=%d", filter, result.Total, len(result.Items), total)
		}
	}
	combined := list("domain=paging.test&probe_status=available&token_status=has_at")
	if combined.Total != 1 || combined.Items[0].Id != id {
		t.Fatalf("combined probe/token filter: %+v", combined)
	}
	emptyTokenPage := list("domain=paging.test&token_status=missing_at&page=4")
	if emptyTokenPage.Total != 44 || len(emptyTokenPage.Items) != 0 {
		t.Fatalf("filtered empty page lost total: %+v", emptyTokenPage)
	}
	direct, err := h.targetPersonalAccess(ctx, id)
	if err != nil || direct.Status != saved.Items[0].PersonalAccess.Status {
		t.Fatalf("page and direct login facts differ: %+v %v", direct, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE tsw_target_personal_sessions SET expires_at=now()-interval '1 second' WHERE target_account_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	expired := list("domain=paging.test&search=account00")
	if expired.Items[0].PersonalAccess.Status != "session_expired" || expired.Items[0].PersonalAccess.ExpiresAt != nil {
		t.Fatalf("expired login was shown as ready: %+v", expired)
	}
	if !expired.Items[0].TokenStatus.HasAccessToken || list("domain=paging.test&token_status=has_at").Total != 1 {
		t.Fatal("expiry must not erase saved AT presence")
	}
	if _, err = pool.Exec(ctx, `UPDATE tsw_target_personal_access SET status='verifying',checked_at=now()-interval '2 minutes' WHERE target_account_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	interrupted := list("domain=paging.test&search=account00")
	if interrupted.Items[0].PersonalAccess.Status != "refresh_failed" {
		t.Fatalf("interrupted login remained running: %+v", interrupted)
	}
	if _, err = pool.Exec(ctx, `UPDATE tsw_target_personal_access SET secret_revision=secret_revision+1,status='ready' WHERE target_account_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	changed := list("domain=paging.test&search=account00")
	if changed.Items[0].PersonalAccess.Status != "not_verified" {
		t.Fatalf("changed credentials reused old login: %+v", changed)
	}
	if changed.Items[0].HasPassword == false || changed.Items[0].HasTotp == false {
		t.Fatal("login metadata changed account material flags")
	}
	password, _, _, _, err := h.sealTargetFields("rotated-password", "JBSWY3DPEHPK3PXP", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE tsw_target_credentials SET password_secret=$2,secret_revision=secret_revision+1,version=version+1 WHERE target_account_id=$1`, id, password); err != nil {
		t.Fatal(err)
	}
	if list("domain=paging.test&token_status=has_at").Total != 0 {
		t.Fatal("invalidated Personal session still appeared in AT filter")
	}
}

func TestAccountOAuthTokenPresenceFilters(t *testing.T) {
	pool, h, _, session, _ := childReviewFixture(t)
	ctx := context.Background()
	var asset, workspace string
	if err := pool.QueryRow(ctx, `SELECT asset.id,version.validated_workspace_id FROM tsw_oauth_assets asset JOIN tsw_delivery_versions version ON version.id=asset.current_delivery_version_id`).Scan(&asset, &workspace); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		payload string
		at, rt  bool
	}{
		{`{"access_token":"test-only-at"}`, true, false},
		{`{"refresh_token":"test-only-rt"}`, false, true},
		{`{"access_token":"test-both-at","refresh_token":"test-both-rt"}`, true, true},
		{`{"access_token":"  ","refresh_token":null}`, false, false},
	}
	for index, test := range cases {
		version := uuid.New()
		if _, err := pool.Exec(ctx, `INSERT INTO tsw_delivery_versions(id,oauth_asset_id,generation,payload,payload_sha256,validated_platform_subject_id,validated_workspace_id)
 VALUES ($1,$2,$3,$4,decode(repeat('11',32),'hex'),'subject',$5)`, version, asset, index+2, test.payload, workspace); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE tsw_oauth_assets SET current_generation=$2,current_delivery_version_id=$3 WHERE id=$1`, asset, index+2, version); err != nil {
			t.Fatal(err)
		}
		for _, filter := range []string{"", "has_at", "missing_at", "has_rt", "missing_rt", "both", "none"} {
			r := httptest.NewRequest("GET", "/api/owner/v1/target-accounts?token_status="+filter, nil)
			r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
			w := httptest.NewRecorder()
			ownerapi.Handler(h).ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("list status=%d: %s", w.Code, w.Body.String())
			}
			var result ownerapi.TargetAccountList
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			match := filter == "" || filter == "has_at" && test.at || filter == "missing_at" && !test.at || filter == "has_rt" && test.rt || filter == "missing_rt" && !test.rt || filter == "both" && test.at && test.rt || filter == "none" && !test.at && !test.rt
			want := int64(0)
			if match {
				want = 1
			}
			if result.Total != want || len(result.Items) != int(want) {
				t.Fatalf("current OAuth case=%d filter=%s: %+v", index, filter, result)
			}
			if match && (result.Items[0].TokenStatus == nil || result.Items[0].TokenStatus.HasAccessToken != test.at || result.Items[0].TokenStatus.HasRefreshToken != test.rt) {
				t.Fatalf("token flags differ from filter: %+v", result.Items[0])
			}
		}
	}
	r := httptest.NewRequest("GET", "/api/owner/v1/target-accounts?token_status=invalid", nil)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
	w := httptest.NewRecorder()
	ownerapi.Handler(h).ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("invalid token filter status=%d", w.Code)
	}
}
