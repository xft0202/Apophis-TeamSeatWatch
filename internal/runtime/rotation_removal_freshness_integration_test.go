//go:build integration

package runtime

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestSlotRemovalReleasedViewAndProgressRequireThirtySecondEvidence(t *testing.T) {
	f := newRemovalFixture(t, 1)
	p := f.start(t)
	first := f.action(t, p.Slots[0].Id, "run")
	if !first.Slots[0].CandidateReady || first.Slots[0].VerificationId == nil {
		t.Fatalf("fresh committed evidence did not release slot: %+v", first)
	}
	slotID, originalEvidence := p.Slots[0].Id, *first.Slots[0].VerificationId
	reads, requests := f.http.reads, f.http.calls
	for _, tc := range []struct {
		name      string
		age       int
		wantReady bool
	}{
		{"fresh", 10, true},
		{"past_thirty_seconds", 31, false},
		{"ninety_seconds", 90, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Model elapsed observation time without sleeps or rewriting immutable
			// evidence. The copied row retains all original identity/authorization
			// bindings; only its observation timestamp is older.
			evidenceID := uuid.New()
			f.exec(t, `INSERT INTO tsw_rotation_removal_evidence(id,slot_id,lease_epoch,authorization_digest,token_exchange_id,owner_evidence_id,observed_at,members,target_absent,complete)
SELECT $1,slot_id,lease_epoch,authorization_digest,token_exchange_id,owner_evidence_id,clock_timestamp()-make_interval(secs => $3::double precision),members,target_absent,complete FROM tsw_rotation_removal_evidence WHERE id=$2`, evidenceID, originalEvidence, tc.age)
			f.exec(t, `UPDATE tsw_rotation_removal_slots SET verification_id=$2,absent_verified_at=(SELECT observed_at FROM tsw_rotation_removal_evidence WHERE id=$2) WHERE id=$1`, slotID, evidenceID)
			var released bool
			if err := f.pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM tsw_rotation_released_slots WHERE id=$1)`, slotID).Scan(&released); err != nil {
				t.Fatal(err)
			}
			if released != tc.wantReady {
				t.Errorf("released view accepted stale proof: age=%ds ready=%v want=%v", tc.age, released, tc.wantReady)
			}
			progress := removalResult(t, removalRequest(f.h, f.session, f.csrf, "GET", f.preview.Id, uuid.Nil, "get", nil), 200)
			if progress.Slots[0].CandidateReady != tc.wantReady || progress.Slots[0].State != "absent_verified" || progress.Slots[0].RemoteRequestId == nil || progress.Slots[0].UncertainObligation {
				t.Errorf("progress lost original obligation or exposed stale readiness: age=%ds %+v", tc.age, progress)
			}
			if f.http.reads != reads || f.http.calls != requests {
				t.Fatal("read-only progress silently refreshed evidence or sent DELETE")
			}
		})
	}
	refreshed := f.action(t, slotID, "verify")
	if !refreshed.Slots[0].CandidateReady || refreshed.Slots[0].VerificationId == nil || *refreshed.Slots[0].VerificationId == originalEvidence || f.http.calls != requests {
		t.Fatalf("explicit verification did not renew proof without another DELETE: %+v", refreshed)
	}
}
