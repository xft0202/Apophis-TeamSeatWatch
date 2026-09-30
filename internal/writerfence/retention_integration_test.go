//go:build integration

package writerfence

import (
	"github.com/google/uuid"
	"testing"
)

func TestExpiredVerificationDeletionEpochIntegration(t *testing.T) {
	db, ctx := epochFixture(t)
	mother, workspace := uuid.New(), uuid.New()
	execEpoch(t, ctx, db, `INSERT INTO tsw_mother_accounts(id,display_name) VALUES($1,'retention mother')`, mother)
	execEpoch(t, ctx, db, `INSERT INTO tsw_workspaces(id,platform_workspace_id,display_name) VALUES($1,$2,'retention space')`, workspace, workspace.String())
	var id int64
	if err := db.QueryRow(ctx, `INSERT INTO tsw_workspace_verifications(workspace_id,mother_account_id,discovery_run_id,session_generation,secret_revision,token_attempt,token_exchange_id,source,outcome,permission,completeness,observed_at,expires_at) VALUES($1,$2,$3,$4,1,1,$5,'injected_platform_reader','failed','unknown','unknown',now()-interval '1 day',now()-interval '1 second') RETURNING id`, workspace, mother, uuid.New(), uuid.New(), uuid.New()).Scan(&id); err != nil {
		t.Fatal(err)
	}
	execEpoch(t, ctx, db, `INSERT INTO tsw_workspace_verification_entries(verification_id,kind,identifier,identifier_hmac,identifier_key_version,status,platform_member_id) VALUES($1,'member','member@fixture.test',decode(repeat('75',32),'hex'),1,'active','member-id')`, id)
	key := Key{Workspace, workspace}
	if epochVersion(t, ctx, db, key) != 3 {
		t.Fatal("verification + entry insert not fenced")
	}
	execEpoch(t, ctx, db, `DELETE FROM tsw_workspace_verification_entries WHERE verification_id=$1`, id)
	execEpoch(t, ctx, db, `DELETE FROM tsw_workspace_verifications WHERE id=$1`, id)
	if epochVersion(t, ctx, db, key) != 5 {
		t.Fatal("expired entry + verification deletion not fenced")
	}
}
