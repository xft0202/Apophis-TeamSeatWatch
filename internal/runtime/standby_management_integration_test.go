//go:build integration

package runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

func TestStandbyManagementPaginationMembershipFiltersAndRename(t *testing.T) {
	pool, h, owner, session, csrf := childReviewFixture(t)
	ctx := context.Background()
	batches := make([]uuid.UUID, 123)
	for i := range batches {
		if err := pool.QueryRow(ctx, `INSERT INTO tsw_standby_child_batches(name) VALUES($1) RETURNING id`, fmt.Sprintf("batch-page-%03d", i)).Scan(&batches[i]); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	ids := make([]uuid.UUID, 45)
	for i := range ids {
		address := fmt.Sprintf("batchuser-%02d@batchpage.test", i)
		item, err := h.insertTargetAccountTx(httptest.NewRequest("POST", "/", nil), tx, address, address, "private-password", "JBSWY3DPEHPK3PXP", "", "")
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = item.Id
		if i < 10 {
			if _, err = tx.Exec(ctx, `INSERT INTO tsw_standby_child_memberships(target_account_id,batch_id) VALUES($1,$2)`, item.Id, batches[0]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var probeBatch uuid.UUID
	if err = pool.QueryRow(ctx, `INSERT INTO tsw_personal_probe_batches(owner_id,request_key,scope_hash,scope_key_version,confirmed_scope_token,scope,scope_label,total)
 VALUES ($1,'batch-page-probes','fixture',1,'fixture','selected','fixture',45) RETURNING id`, owner).Scan(&probeBatch); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO tsw_personal_probe_items(batch_id,target_account_id,identifier,status,outcome,verified_evidence,finished_at)
 SELECT $1,id,identifier,'succeeded','available',true,clock_timestamp() FROM tsw_target_accounts WHERE id=ANY($2::uuid[])`, probeBatch, ids); err != nil {
		t.Fatal(err)
	}

	router := ownerapi.Handler(h)
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	path := "/api/owner/v1/standby-child-batches"
	for _, size := range []int{20, 50, 100} {
		page := decodeStandby[ownerapi.StandbyChildBatchList](t, get(fmt.Sprintf("%s?search=batch-page&page=2&page_size=%d", path, size)), 200)
		expected := size
		if expected > 123-size {
			expected = 123 - size
		}
		if page.Total != 123 || page.Page != 2 || page.PageSize != size || len(page.Items) != expected {
			t.Fatalf("page=%+v", page)
		}
		for _, batch := range page.Items {
			if batch.UpdatedAt.IsZero() {
				t.Fatal("missing true update time")
			}
		}
	}
	exact := decodeStandby[ownerapi.StandbyChildBatchList](t, get(path+"?domain=%40BATCHPAGE.TEST"), 200)
	if exact.Total != 1 || exact.Items[0].Id != batches[0] || exact.Items[0].MemberCount != 10 {
		t.Fatalf("domain filtering=%+v", exact)
	}
	unassigned := decodeStandby[ownerapi.TargetAccountList](t, get("/api/owner/v1/target-accounts?domain=batchpage.test&membership_status=unassigned&probe_status=available&page_size=20"), 200)
	if unassigned.Total != 35 || len(unassigned.Items) != 20 {
		t.Fatalf("unassigned=%+v", unassigned)
	}
	for _, item := range unassigned.Items {
		if item.StandbyBatch != nil {
			t.Fatal("unassigned has a batch")
		}
	}
	members := decodeStandby[ownerapi.TargetAccountList](t, get("/api/owner/v1/target-accounts?standby_batch_id="+batches[0].String()), 200)
	if members.Total != 10 {
		t.Fatalf("members count=%d", members.Total)
	}
	for _, item := range members.Items {
		if item.StandbyBatch == nil || item.StandbyBatch.Id != batches[0] || item.StandbyBatch.Name != "batch-page-000" || item.TokenStatus == nil {
			t.Fatalf("missing member facts=%+v", item)
		}
	}
	memberSelection := decodeStandby[ownerapi.StandbyChildSelection](t, standbyRequest(t, h, session, csrf, "POST", path+"/selection", map[string]any{"scope": "filtered", "batchId": batches[0], "search": "batchuser-0", "probeStatus": "available"}), 200)
	if memberSelection.Count != 10 {
		t.Fatalf("filtered member scope=%+v", memberSelection)
	}

	excluded := decodeStandby[ownerapi.TargetAccountList](t, get("/api/owner/v1/target-accounts?domain=batchpage.test&exclude_standby_batch_id="+batches[0].String()), 200)
	if excluded.Total != 35 {
		t.Fatalf("exclude count=%d", excluded.Total)
	}
	preview := decodeStandby[ownerapi.StandbyChildSelection](t, standbyRequest(t, h, session, csrf, "POST", path+"/selection", map[string]any{"scope": "filtered", "domain": "@BATCHPAGE.TEST", "probeStatus": "available", "membershipStatus": "unassigned", "excludeBatchId": batches[0]}), 200)
	if preview.Count != int(unassigned.Total) {
		t.Fatalf("filter scope mismatch: preview=%d list=%d", preview.Count, unassigned.Total)
	}
	assigned := decodeStandby[ownerapi.StandbyChildSelection](t, standbyRequest(t, h, session, csrf, "POST", path+"/selection", map[string]any{"scope": "selected", "accountIds": ids[:1]}), 200)
	if assigned.Members[0].CurrentBatch == nil || assigned.Members[0].CurrentBatch.Id != batches[0] || assigned.Members[0].CurrentBatch.Name != "batch-page-000" {
		t.Fatalf("transfer origin=%+v", assigned.Members[0])
	}
	original := decodeStandby[ownerapi.StandbyChildBatch](t, get(path+"/"+batches[0].String()), 200)
	renamed := decodeStandby[ownerapi.StandbyChildBatch](t, standbyRequest(t, h, session, csrf, "PATCH", path+"/"+batches[0].String()+"/name", map[string]any{"name": "renamed-page", "expectedVersion": original.Version}), 200)
	if renamed.Name != "renamed-page" || renamed.Version != original.Version+1 || renamed.MemberCount != original.MemberCount || renamed.UpdatedAt.Before(original.UpdatedAt) {
		t.Fatalf("rename=%+v", renamed)
	}
	stale := standbyRequest(t, h, session, csrf, "PATCH", path+"/"+batches[0].String()+"/name", map[string]any{"name": "wrong", "expectedVersion": original.Version})
	if stale.Code != 409 {
		t.Fatalf("stale rename accepted: %d", stale.Code)
	}
	var actual int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM tsw_standby_child_memberships WHERE batch_id=$1`, batches[0]).Scan(&actual); err != nil || actual != 10 {
		t.Fatalf("rename altered members=%d err=%v", actual, err)
	}
}
