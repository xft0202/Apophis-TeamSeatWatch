//go:build integration

package runtime

import (
	"bytes"
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	targetdomain "github.com/xft0202/Apophis-TeamSeatWatch/internal/target"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/workspace"
)

func TestOperationDraftExpiredVerificationRetentionIntegration(t *testing.T) {
	pool, h, _, session, csrf := childReviewFixture(t)
	ctx := context.Background()
	mother, space, batch, child := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	run, generation, exchange := uuid.New(), uuid.New(), uuid.New()
	password, err := targetdomain.SealMaterial("password", h.keyRing)
	if err != nil {
		t.Fatal(err)
	}
	totp, err := targetdomain.SealMaterial("JBSWY3DPEHPK3PXP", h.keyRing)
	if err != nil {
		t.Fatal(err)
	}
	seed := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO tsw_mother_accounts(id,display_name) VALUES($1,'retention mother')`, []any{mother}},
		{`INSERT INTO tsw_mother_account_credentials(mother_account_id,login_identifier,identifier_hmac,identifier_key_version,password_secret,totp_secret) VALUES($1,'retention@fixture.test',decode(repeat('51',32),'hex'),1,'pw','totp')`, []any{mother}},
		{`INSERT INTO tsw_workspaces(id,platform_workspace_id,display_name) VALUES($1,$2,'retention space')`, []any{space, space.String()}},
		{`INSERT INTO tsw_workspace_projections(workspace_id,operational_state) VALUES($1,'unknown')`, []any{space}},
		{`INSERT INTO tsw_mother_personal_sessions(mother_account_id,secret_revision,generation,key_version,nonce,sealed_session,expires_at) VALUES($1,1,$2,1,decode(repeat('11',12),'hex'),decode(repeat('22',32),'hex'),now()+interval '1 day')`, []any{mother, generation}},
		{`INSERT INTO tsw_mother_discoveries(mother_account_id,run_id,secret_revision,session_generation,status) VALUES($1,$2,1,$3,'discovered')`, []any{mother, run, generation}},
		{`INSERT INTO tsw_mother_workspace_visibility(mother_account_id,workspace_id,run_id,access_status) VALUES($1,$2,$3,'readable')`, []any{mother, space, run}},
		{`INSERT INTO tsw_selected_workspace_tokens(mother_account_id,workspace_id,discovery_run_id,session_generation,secret_revision,exchange_id,status,key_version,nonce,sealed_access,expires_at) VALUES($1,$2,$3,$4,1,$5,'ready',1,decode(repeat('33',12),'hex'),decode(repeat('44',32),'hex'),now()+interval '1 day')`, []any{mother, space, run, generation, exchange}},
		{`INSERT INTO tsw_standby_child_batches(id,name) VALUES($1,'retention batch')`, []any{batch}},
		{`INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,'retention-child@fixture.test',$2,1,'retention child')`, []any{child, bytes.Repeat([]byte{0x71}, 32)}},
		{`INSERT INTO tsw_target_credentials(target_account_id,password_secret,totp_secret,material_status,materials_sealed) VALUES($1,$2,$3,'complete',true)`, []any{child, password, totp}},
		{`INSERT INTO tsw_standby_child_memberships(target_account_id,batch_id) VALUES($1,$2)`, []any{child, batch}},
	}
	for _, row := range seed {
		if _, err := pool.Exec(ctx, row.query, row.args...); err != nil {
			t.Fatalf("seed: %v (%s)", err, row.query)
		}
	}
	// Keep the fact valid throughout draft creation, regardless of CI speed.
	var verificationID int64
	err = pool.QueryRow(ctx, `INSERT INTO tsw_workspace_verifications(workspace_id,mother_account_id,discovery_run_id,session_generation,secret_revision,token_attempt,token_exchange_id,source,outcome,permission,completeness,observed_at,expires_at,active_until,seat_limit,member_count,pending_invite_count) VALUES($1,$2,$3,$4,1,1,$5,'injected_platform_reader','verified','read','complete',now(),now()+interval '1 day',now()+interval '30 days',20,0,0) RETURNING id`, space, mother, run, generation, exchange).Scan(&verificationID)
	if err != nil {
		t.Fatal(err)
	}
	start := draftResult(t, operationDraftRequest(t, h, session, csrf, "POST", nil), 200)
	change := func(d ownerapi.OperationDraft, choice string, fields map[string]any) ownerapi.OperationDraft {
		body := map[string]any{"expectedVersion": d.Version, "choice": choice}
		for name, value := range fields {
			body[name] = value
		}
		return draftResult(t, operationDraftRequest(t, h, session, csrf, "PATCH", body), 200)
	}
	d := change(start, "mother", map[string]any{"motherAccountId": mother})
	d = change(d, "workspace", map[string]any{"workspaceId": space})
	d = change(d, "children", map[string]any{"batchId": batch, "batchVersion": 1, "children": []map[string]any{{"accountId": child, "membershipVersion": 1}}})
	if d.VerificationId == nil || *d.VerificationId != verificationID || !d.BatchCurrent {
		t.Fatalf("workspace draft not verified: %+v", d)
	}
	// In this disposable database only, atomically age the saved fact after
	// selection. Rollback restores the trigger on every error path; commit only
	// after re-enabling it, before exercising real production retention.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `ALTER TABLE tsw_workspace_verifications DISABLE TRIGGER tsw_workspace_verifications_immutable`); err != nil {
		t.Fatal(err)
	}
	updated, err := tx.Exec(ctx, `UPDATE tsw_workspace_verifications SET observed_at=now()-interval '2 hours',expires_at=now()-interval '1 hour' WHERE id=$1`, verificationID)
	if err != nil || updated.RowsAffected() != 1 {
		t.Fatalf("expire disposable verification: affected=%d err=%v", updated.RowsAffected(), err)
	}
	if _, err = tx.Exec(ctx, `ALTER TABLE tsw_workspace_verifications ENABLE TRIGGER tsw_workspace_verifications_immutable`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var triggerState string
	if err = pool.QueryRow(ctx, `SELECT tgenabled FROM pg_trigger WHERE tgrelid='tsw_workspace_verifications'::regclass AND tgname='tsw_workspace_verifications_immutable'`).Scan(&triggerState); err != nil || triggerState != "O" {
		t.Fatalf("immutable trigger must be enabled before retention: state=%q err=%v", triggerState, err)
	}
	cleaned, err := workspace.NewService(pool, nil).DeleteExpired(ctx, 10)
	if err != nil {
		t.Fatalf("real workspace retention blocked by frozen draft: %v", err)
	}
	if cleaned != 1 {
		t.Fatalf("cleaned %d workspace(s), want 1", cleaned)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tsw_workspace_verifications WHERE id=$1`, verificationID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("expired verification retained: count=%d err=%v", remaining, err)
	}
	// A newer successful read of the same space cannot silently replace the
	// verification ID frozen in this dormant draft after retention.
	var replacementID int64
	err = pool.QueryRow(ctx, `INSERT INTO tsw_workspace_verifications(workspace_id,mother_account_id,discovery_run_id,session_generation,secret_revision,token_attempt,token_exchange_id,source,outcome,permission,completeness,observed_at,expires_at,active_until,seat_limit,member_count,pending_invite_count) VALUES($1,$2,$3,$4,1,1,$5,'injected_platform_reader','verified','read','complete',now(),now()+interval '1 day',now()+interval '30 days',20,0,0) RETURNING id`, space, mother, run, generation, exchange).Scan(&replacementID)
	if err != nil || replacementID == verificationID {
		t.Fatalf("replacement verification: id=%d err=%v", replacementID, err)
	}
	stale := draftResult(t, operationDraftRequest(t, h, session, csrf, "GET", nil), 200)
	if stale.Id != d.Id || stale.Version != d.Version || stale.Step != "ready" || stale.VerificationId == nil || *stale.VerificationId != verificationID || stale.WorkspaceCurrent || stale.BatchCurrent {
		t.Fatalf("cleanup rewrote draft or reused deleted verification: %+v", stale)
	}
	if stale.MotherAccountId == nil || *stale.MotherAccountId != mother || !stale.MotherCurrent || stale.WorkspaceId == nil || *stale.WorkspaceId != space || stale.BatchId == nil || *stale.BatchId != batch || len(stale.Children) != 1 || stale.Children[0].AccountId != child {
		t.Fatalf("cleanup lost unaffected frozen selection: %+v", stale)
	}
	blocked := operationDraftRequest(t, h, session, csrf, "PATCH", map[string]any{"expectedVersion": stale.Version, "choice": "children", "batchId": batch, "batchVersion": 1, "children": []map[string]any{{"accountId": child, "membershipVersion": 1}}})
	if blocked.Code != 409 {
		t.Fatalf("expired verification authorized downstream selection: %d %s", blocked.Code, blocked.Body.String())
	}
}
