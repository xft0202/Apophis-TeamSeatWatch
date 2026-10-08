//go:build integration

package runtime

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func (f *removalFixture) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), q, args...); err != nil {
		t.Fatal(err)
	}
}
func (f *removalFixture) rejectAction(t *testing.T, slot uuid.UUID) {
	t.Helper()
	before := f.http.calls
	w := removalRequest(f.h, f.session, f.csrf, "POST", f.preview.Id, slot, "run", map[string]any{"confirmed": true})
	if w.Code != 409 && w.Code != 404 {
		t.Fatalf("unsafe action not rejected: %d %s", w.Code, w.Body.String())
	}
	if f.http.calls != before {
		t.Fatal("rejected action dispatched DELETE")
	}
}
func (f *removalFixture) tamperPreview(t *testing.T, q string, args ...any) {
	t.Helper()
	f.exec(t, `ALTER TABLE tsw_expiry_rotation_previews DISABLE TRIGGER tsw_expiry_rotation_preview_guard`)
	f.exec(t, q, args...)
	f.exec(t, `ALTER TABLE tsw_expiry_rotation_previews ENABLE TRIGGER tsw_expiry_rotation_preview_guard`)
}

func TestSlotRemovalAuthorizationDriftFailsClosed(t *testing.T) {
	cases := []struct {
		name   string
		change func(*testing.T, *removalFixture, ownerapi.RotationRemoval)
	}{
		{"revoked", func(t *testing.T, f *removalFixture, _ ownerapi.RotationRemoval) {
			rotationResult(t, rotationRequest(f.h, f.session, f.csrf, "POST", "/api/owner/v1/expiry-rotation/previews/"+f.preview.Id.String()+"/revoke", nil), 200)
		}},
		{"expired", func(t *testing.T, f *removalFixture, _ ownerapi.RotationRemoval) {
			f.tamperPreview(t, `UPDATE tsw_expiry_rotation_previews SET expires_at=now()-interval '1 second' WHERE id=$1`, f.preview.Id)
		}},
		{"digest", func(t *testing.T, f *removalFixture, _ ownerapi.RotationRemoval) {
			f.tamperPreview(t, `UPDATE tsw_expiry_rotation_previews SET authorization_digest=repeat('f',64) WHERE id=$1`, f.preview.Id)
		}},
		{"global_protection", func(t *testing.T, f *removalFixture, p ownerapi.RotationRemoval) {
			f.exec(t, `INSERT INTO tsw_rotation_global_protections(target_account_id,status,evidence_source,evidence_id,observed_at) VALUES($1,'delivered','fixture',repeat('c',64),now())`, p.Slots[0].OriginalAccountId)
		}},
		{"usage_evidence", func(t *testing.T, f *removalFixture, p ownerapi.RotationRemoval) {
			f.exec(t, `UPDATE tsw_rotation_usage_ledger SET evidence_id=repeat('c',64),version=version+1 WHERE target_account_id=$1 AND workspace_id=$2`, p.Slots[0].OriginalAccountId, f.space)
		}},
		{"owner_role_changed_and_restored", func(t *testing.T, f *removalFixture, _ ownerapi.RotationRemoval) {
			f.exec(t, `UPDATE tsw_mother_workspace_visibility SET workspace_role='member' WHERE workspace_id=$1`, f.space)
			f.exec(t, `UPDATE tsw_mother_workspace_visibility SET workspace_role='owner' WHERE workspace_id=$1`, f.space)
		}},
		{"originating_session_revoked", func(t *testing.T, f *removalFixture, _ ownerapi.RotationRemoval) {
			f.exec(t, `UPDATE tsw_owner_sessions SET revoked_at=now(),revocation_reason='fixture' WHERE id=(SELECT authorized_session::uuid FROM tsw_expiry_rotation_previews WHERE id=$1)`, f.preview.Id)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRemovalFixture(t, 1)
			p := f.start(t)
			tc.change(t, f, p)
			w := removalRequest(f.h, f.session, f.csrf, "POST", f.preview.Id, p.Slots[0].Id, "run", map[string]any{"confirmed": true})
			if w.Code != 409 && w.Code != 401 {
				t.Fatalf("drift accepted: %d %s", w.Code, w.Body.String())
			}
			if f.http.calls != 0 {
				t.Fatal("authorization drift dispatched DELETE")
			}
		})
	}
}
func TestSlotRemovalEveryFrozenEpochIsChecked(t *testing.T) {
	f := newRemovalFixture(t, 1)
	p := f.start(t)
	rows, err := f.pool.Query(context.Background(), `SELECT key,value FROM jsonb_each_text((SELECT epoch_versions FROM tsw_expiry_rotation_previews WHERE id=$1))`, f.preview.Id)
	if err != nil {
		t.Fatal(err)
	}
	epochs := map[string]string{}
	for rows.Next() {
		var k, v string
		if err = rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		epochs[k] = v
	}
	rows.Close()
	if len(epochs) < 9 {
		t.Fatalf("incomplete frozen scopes %v", epochs)
	}
	for scope, version := range epochs {
		t.Run(scope, func(t *testing.T) {
			f.exec(t, `UPDATE tsw_rotation_epochs SET version=version+1 WHERE kind||'/'||id::text=$1`, scope)
			f.rejectAction(t, p.Slots[0].Id)
			f.exec(t, `UPDATE tsw_rotation_epochs SET version=$2::bigint WHERE kind||'/'||id::text=$1`, scope, version)
		})
	}
}
func TestSlotRemovalRemoteIdentitySeatAndOwnerDriftNeverDispatch(t *testing.T) {
	for _, name := range []string{"owner_role", "member_id", "seat_type", "duplicate_account", "alias_conflict"} {
		t.Run(name, func(t *testing.T) {
			f := newRemovalFixture(t, 1)
			p := f.start(t)
			switch name {
			case "owner_role":
				f.http.role = "member"
			case "member_id":
				f.http.members[0].PlatformMemberID = "other-member"
			case "seat_type":
				f.http.members[0].SeatType = "default"
			case "duplicate_account":
				f.http.members = append(f.http.members, f.http.members[0])
			case "alias_conflict":
				f.http.members[0].PlatformAccountUserID = "other-id"
				m := f.http.members[0]
				m.Identifier = "other@remove.test"
				m.PlatformMemberID = "other-id"
				m.PlatformAccountUserID = ""
				f.http.members = append(f.http.members, m)
			}
			out := f.action(t, p.Slots[0].Id, "run")
			if f.http.calls != 0 || out.Slots[0].CandidateReady || out.Slots[0].State != "blocked" {
				t.Fatalf("remote drift accepted: %+v calls=%d", out, f.http.calls)
			}
		})
	}
}
func TestSlotRemovalUnknownSlotAndDifferentKeyAreRejected(t *testing.T) {
	f := newRemovalFixture(t, 1)
	p := f.start(t)
	f.rejectAction(t, uuid.New())
	w := removalRequest(f.h, f.session, f.csrf, "POST", f.preview.Id, uuid.Nil, "start", map[string]any{"confirmed": true, "authorizationDigest": *f.preview.AuthorizationDigest, "idempotencyKey": uuid.New()})
	if w.Code != 409 || f.http.calls != 0 || p.Slots[0].State != "pending" {
		t.Fatal("different key changed removal")
	}
}

func TestSlotRemovalReceiptsNeverReleaseWithoutCompleteReconciliation(t *testing.T) {
	for _, name := range []string{"success_partial", "timeout_present", "timeout_applied", "404", "429", "503"} {
		t.Run(name, func(t *testing.T) {
			f := newRemovalFixture(t, 1)
			p := f.start(t)
			switch name {
			case "success_partial":
				f.http.partialAfter = true
			case "timeout_present":
				f.http.fail = context.DeadlineExceeded
				f.http.remove = false
			case "timeout_applied":
				f.http.fail = context.DeadlineExceeded
			case "404":
				f.http.status = 404
			case "429":
				f.http.status = 429
				f.http.remove = false
			case "503":
				f.http.status = 503
				f.http.remove = false
			}
			out := f.action(t, p.Slots[0].Id, "run")
			if out.Slots[0].CandidateReady || out.Slots[0].State == "absent_verified" || !out.Slots[0].UncertainObligation {
				t.Fatalf("receipt released slot: %+v", out)
			}
			request := out.Slots[0].RemoteRequestId
			if request == nil {
				t.Fatal("request obligation missing")
			}
			// Reconstruct the handler after a process restart, retaining only dependencies
			// and durable DB state, not an in-memory slot or completion flag.
			restarted := &OwnerAuthHandler{pool: f.pool, keyRing: f.h.keyRing, origins: f.h.origins, discovery: f.h.discovery, workspaceMemberRemover: f.h.workspaceMemberRemover}
			f.h = restarted
			f.http.partialAfter = false
			f.http.fail = nil
			f.http.status = 204
			after := f.action(t, p.Slots[0].Id, "verify")
			shouldRelease := name == "success_partial" || name == "timeout_applied" || name == "404"
			if after.Slots[0].CandidateReady != shouldRelease || f.http.calls != 1 || after.Slots[0].RemoteRequestId == nil || *after.Slots[0].RemoteRequestId != *request {
				t.Fatalf("reconciliation or request identity changed: %+v calls=%d", after, f.http.calls)
			}
		})
	}
}
func TestSlotRemovalDatabaseEvidenceFailurePreservesOriginalObligation(t *testing.T) {
	f := newRemovalFixture(t, 1)
	p := f.start(t)
	f.exec(t, `CREATE FUNCTION fail_removal_evidence() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected commit failure'; END $$; CREATE TRIGGER fail_removal_evidence BEFORE INSERT ON tsw_rotation_removal_evidence FOR EACH ROW EXECUTE FUNCTION fail_removal_evidence()`)
	w := removalRequest(f.h, f.session, f.csrf, "POST", f.preview.Id, p.Slots[0].Id, "run", map[string]any{"confirmed": true})
	if w.Code != 500 {
		t.Fatalf("DB failure hidden: %d %s", w.Code, w.Body.String())
	}
	history := removalResult(t, removalRequest(f.h, f.session, f.csrf, "GET", f.preview.Id, uuid.Nil, "get", nil), 200)
	if history.Slots[0].CandidateReady || !history.Slots[0].UncertainObligation || history.Slots[0].VerificationId != nil {
		t.Fatalf("rollback released original: %+v", history)
	}
	f.exec(t, `DROP TRIGGER fail_removal_evidence ON tsw_rotation_removal_evidence; DROP FUNCTION fail_removal_evidence()`)
	f.exec(t, `UPDATE tsw_rotation_removal_slots SET lease_expires_at=now()-interval '1 second' WHERE id=$1`, p.Slots[0].Id)
	after := f.action(t, p.Slots[0].Id, "verify")
	if !after.Slots[0].CandidateReady || f.http.calls != 1 {
		t.Fatalf("restart did not reconcile original: %+v", after)
	}
}
func TestSlotRemovalExpiredOrSupersededLeaseCannotCommit(t *testing.T) {
	for _, name := range []string{"expired", "superseded"} {
		t.Run(name, func(t *testing.T) {
			f := newRemovalFixture(t, 1)
			p := f.start(t)
			f.http.onDelete = func() {
				q := `UPDATE tsw_rotation_removal_slots SET lease_expires_at=now()-interval '1 second' WHERE id=$1`
				if name == "superseded" {
					q = `UPDATE tsw_rotation_removal_slots SET lease_token=gen_random_uuid(),lease_epoch=lease_epoch+1,lease_expires_at=now()-interval '1 second' WHERE id=$1`
				}
				f.exec(t, q, p.Slots[0].Id)
			}
			w := removalRequest(f.h, f.session, f.csrf, "POST", f.preview.Id, p.Slots[0].Id, "run", map[string]any{"confirmed": true})
			if w.Code != 409 {
				t.Fatalf("old lease committed: %d %s", w.Code, w.Body.String())
			}
			out := removalResult(t, removalRequest(f.h, f.session, f.csrf, "GET", f.preview.Id, uuid.Nil, "get", nil), 200)
			if out.Slots[0].CandidateReady || !out.Slots[0].UncertainObligation {
				t.Fatalf("expired lease released: %+v", out)
			}
			f.http.onDelete = nil
			after := f.action(t, p.Slots[0].Id, "verify")
			if !after.Slots[0].CandidateReady || f.http.calls != 1 {
				t.Fatal("new lease did not reconcile without delete")
			}
		})
	}
}
func TestSlotRemovalLeaseCannotOutliveItsOwnerSession(t *testing.T) {
	f := newRemovalFixture(t, 1, 10*time.Second)
	p := f.start(t)
	baseline := f.http.reads
	entered, release := make(chan struct{}), make(chan struct{})
	f.http.onSnapshot = func(n int) {
		if n == baseline+1 {
			close(entered)
			<-release
		}
	}
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		result <- removalRequest(f.h, f.session, f.csrf, "POST", f.preview.Id, p.Slots[0].Id, "run", map[string]any{"confirmed": true})
	}()
	<-entered
	var leaseEnd, sessionEnd time.Time
	err := f.pool.QueryRow(context.Background(), `SELECT slot.lease_expires_at,LEAST(session.idle_expires_at,session.absolute_expires_at) FROM tsw_rotation_removal_slots slot JOIN tsw_expiry_rotation_previews preview ON preview.id=slot.preview_id JOIN tsw_owner_sessions session ON session.id=preview.authorized_session::uuid WHERE slot.id=$1`, p.Slots[0].Id).Scan(&leaseEnd, &sessionEnd)
	close(release)
	response := <-result
	if err != nil {
		t.Fatal(err)
	}
	if leaseEnd.After(sessionEnd) {
		t.Fatalf("dispatch lease outlives owner permission: lease=%s session=%s", leaseEnd, sessionEnd)
	}
	removalResult(t, response, 200)
}

func TestSlotRemovalDoubleWorkerHasOneRequestAndLease(t *testing.T) {
	f := newRemovalFixture(t, 1)
	p := f.start(t)
	entered, release := make(chan struct{}), make(chan struct{})
	f.http.onDelete = func() { close(entered); <-release }
	results := make(chan *httptest.ResponseRecorder, 2)
	go func() {
		results <- removalRequest(f.h, f.session, f.csrf, "POST", f.preview.Id, p.Slots[0].Id, "run", map[string]any{"confirmed": true})
	}()
	<-entered
	busy := removalResult(t, removalRequest(f.h, f.session, f.csrf, "GET", f.preview.Id, uuid.Nil, "get", nil), 200)
	if busy.Slots[0].LeaseEpoch != 1 || busy.Slots[0].State != "remove_requested" || busy.Slots[0].CandidateReady {
		t.Fatalf("bad in-flight lease: %+v", busy)
	}
	go func() {
		results <- removalRequest(f.h, f.session, f.csrf, "POST", f.preview.Id, p.Slots[0].Id, "run", map[string]any{"confirmed": true})
	}()
	close(release)
	first, second := <-results, <-results // drain both workers before any failure cleanup
	completed := 0
	for _, response := range []*httptest.ResponseRecorder{first, second} {
		if response.Code == 409 {
			if !strings.Contains(response.Body.String(), "slot_lease_busy") {
				t.Fatalf("unexpected conflict: %s", response.Body.String())
			}
			continue
		}
		out := removalResult(t, response, 200)
		completed++
		if out.Slots[0].LeaseEpoch != 1 || !out.Slots[0].CandidateReady {
			t.Fatalf("competing worker changed lease: %+v", out)
		}
	}
	if completed == 0 || f.http.calls != 1 {
		t.Fatal("two workers deleted twice or no worker completed")
	}
}
func TestSlotRemovalStopAndRevokeSerializeWithIssuedRequest(t *testing.T) {
	for _, action := range []string{"stop", "revoke"} {
		t.Run(action, func(t *testing.T) {
			f := newRemovalFixture(t, 2)
			p := f.start(t)
			entered, release := make(chan struct{}), make(chan struct{})
			f.http.fail = context.DeadlineExceeded
			f.http.onDelete = func() { close(entered); <-release }
			runResult := make(chan *httptest.ResponseRecorder, 1)
			controlResult := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				runResult <- removalRequest(f.h, f.session, f.csrf, "POST", f.preview.Id, p.Slots[0].Id, "run", map[string]any{"confirmed": true})
			}()
			<-entered
			go func() {
				if action == "stop" {
					controlResult <- removalRequest(f.h, f.session, f.csrf, "POST", f.preview.Id, uuid.Nil, "stop", map[string]any{"confirmed": true})
				} else {
					controlResult <- rotationRequest(f.h, f.session, f.csrf, "POST", "/api/owner/v1/expiry-rotation/previews/"+f.preview.Id.String()+"/revoke", nil)
				}
			}()
			select {
			case result := <-controlResult:
				t.Fatalf("control returned while dispatch gate still held: %d", result.Code)
			case <-time.After(100 * time.Millisecond):
			}
			close(release)
			out := removalResult(t, <-runResult, 200)
			if !out.Slots[0].UncertainObligation {
				t.Fatal("sent obligation discarded")
			}
			control := <-controlResult
			if control.Code != 200 {
				t.Fatalf("control failed: %d %s", control.Code, control.Body.String())
			}
			f.http.onDelete = nil
			f.rejectAction(t, p.Slots[1].Id)
			after := removalResult(t, removalRequest(f.h, f.session, f.csrf, "GET", f.preview.Id, uuid.Nil, "get", nil), 200)
			if !after.Slots[0].UncertainObligation || after.Slots[0].CandidateReady || f.http.calls != 1 {
				t.Fatalf("stop/revoke released uncertainty: %+v", after)
			}
		})
	}
}
func TestSlotRemovalOneFailureDoesNotRestartOtherSlots(t *testing.T) {
	f := newRemovalFixture(t, 2)
	p := f.start(t)
	f.http.fail = errors.New("connection interrupted")
	f.http.remove = false
	failed := f.action(t, p.Slots[0].Id, "run")
	if failed.Slots[0].CandidateReady {
		t.Fatal("failed slot released")
	}
	f.http.fail = nil
	f.http.remove = true
	done := f.action(t, p.Slots[1].Id, "run")
	if done.Slots[0].CandidateReady || !done.Slots[1].CandidateReady || f.http.calls != 2 {
		t.Fatalf("per-slot results lost: %+v", done)
	}
	f.action(t, p.Slots[0].Id, "verify")
	if f.http.calls != 2 {
		t.Fatal("verification restarted batch")
	}
}
func TestSlotRemovalStopAndRevokeFenceRemotePreflightAndVerification(t *testing.T) {
	for _, action := range []string{"stop", "revoke"} {
		for _, phase := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/read_%d", action, phase), func(t *testing.T) {
				f := newRemovalFixture(t, 1)
				p := f.start(t)
				baseline := f.http.reads
				entered, release := make(chan struct{}), make(chan struct{})
				f.http.onSnapshot = func(n int) {
					if n == baseline+phase {
						close(entered)
						<-release
					}
				}
				run := make(chan *httptest.ResponseRecorder, 1)
				control := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					run <- removalRequest(f.h, f.session, f.csrf, "POST", f.preview.Id, p.Slots[0].Id, "run", map[string]any{"confirmed": true})
				}()
				<-entered
				go func() {
					if action == "stop" {
						control <- removalRequest(f.h, f.session, f.csrf, "POST", f.preview.Id, uuid.Nil, "stop", map[string]any{"confirmed": true})
					} else {
						control <- rotationRequest(f.h, f.session, f.csrf, "POST", "/api/owner/v1/expiry-rotation/previews/"+f.preview.Id.String()+"/revoke", nil)
					}
				}()
				var stopped *httptest.ResponseRecorder
				select {
				case stopped = <-control:
				case <-time.After(2 * time.Second):
					close(release)
					<-run
					<-control
					t.Fatal("stop/revoke was blocked by a read-only preflight")
				}
				close(release)
				if stopped.Code != 200 {
					t.Fatalf("control failed: %d", stopped.Code)
				}
				old := <-run
				if old.Code != 409 {
					t.Fatalf("old worker was not fenced: %d %s", old.Code, old.Body.String())
				}
				after := removalResult(t, removalRequest(f.h, f.session, f.csrf, "GET", f.preview.Id, uuid.Nil, "get", nil), 200)
				if after.Slots[0].CandidateReady || f.http.calls != phase-1 || after.Slots[0].UncertainObligation != (phase == 2) {
					t.Fatalf("fenced worker dispatched or released: %+v calls=%d", after, f.http.calls)
				}
			})
		}
	}
}

func TestSlotRemovalStoppedAndRevokedRequestsRemainReadOnlyReconcilable(t *testing.T) {
	for _, action := range []string{"stop", "revoke"} {
		t.Run(action, func(t *testing.T) {
			f := newRemovalFixture(t, 1)
			p := f.start(t)
			f.http.fail = context.DeadlineExceeded
			uncertain := f.action(t, p.Slots[0].Id, "run")
			if !uncertain.Slots[0].UncertainObligation {
				t.Fatal("missing obligation")
			}
			if action == "stop" {
				f.action(t, uuid.Nil, "stop")
			} else {
				rotationResult(t, rotationRequest(f.h, f.session, f.csrf, "POST", "/api/owner/v1/expiry-rotation/previews/"+f.preview.Id.String()+"/revoke", nil), 200)
			}
			f.http.fail = nil
			reconciled := f.action(t, p.Slots[0].Id, "verify")
			if reconciled.Slots[0].VerificationId == nil || reconciled.Slots[0].CandidateReady || !reconciled.Slots[0].UncertainObligation || f.http.calls != 1 {
				t.Fatalf("read-only reconciliation lost obligation or released: %+v", reconciled)
			}
		})
	}
}

func TestSlotRemovalFactWriterCannotCommitAcrossDispatch(t *testing.T) {
	f := newRemovalFixture(t, 2)
	p := f.start(t)
	entered, release := make(chan struct{}), make(chan struct{})
	f.http.onDelete = func() { close(entered); <-release }
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		result <- removalRequest(f.h, f.session, f.csrf, "POST", f.preview.Id, p.Slots[0].Id, "run", map[string]any{"confirmed": true})
	}()
	<-entered
	var wg sync.WaitGroup
	wg.Add(1)
	writer := make(chan error, 1)
	go func() {
		defer wg.Done()
		_, err := f.pool.Exec(context.Background(), `INSERT INTO tsw_rotation_global_protections(target_account_id,status,evidence_source,evidence_id,observed_at) VALUES($1,'suspected_sold','fixture',repeat('d',64),now())`, p.Slots[0].OriginalAccountId)
		writer <- err
	}()
	select {
	case err := <-writer:
		t.Fatalf("fact committed across dispatch: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	removalResult(t, <-result, 200)
	wg.Wait()
	if err := <-writer; err != nil {
		t.Fatal(err)
	}
	f.http.onDelete = nil
	f.rejectAction(t, p.Slots[1].Id)
}
