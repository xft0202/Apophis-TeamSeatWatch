//go:build integration

package writerfence

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCrossWorkspaceFirstDeliveryIntegration(t *testing.T) {
	db, ctx := epochFixture(t)
	owner, mother, firstSpace, otherSpace, binding, batch, account, operation, target, membership, asset := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	execEpoch(t, ctx, db, `INSERT INTO tsw_owners(id,username,password_hash,totp_ciphertext,totp_nonce,totp_key_version) VALUES($1,'owner','hash',decode(repeat('11',16),'hex'),decode(repeat('22',12),'hex'),1)`, owner)
	execEpoch(t, ctx, db, `INSERT INTO tsw_mother_accounts(id,display_name) VALUES($1,'mother')`, mother)
	execEpoch(t, ctx, db, `INSERT INTO tsw_workspaces(id,platform_workspace_id,display_name) VALUES($1,$2,'first'),($3,$4,'other')`, firstSpace, firstSpace.String(), otherSpace, otherSpace.String())
	execEpoch(t, ctx, db, `INSERT INTO tsw_mother_workspace_bindings(id,mother_account_id,workspace_id) VALUES($1,$2,$3)`, binding, mother, otherSpace)
	execEpoch(t, ctx, db, `INSERT INTO tsw_batches(id,binding_id,sequence_no,planned_at) VALUES($1,$2,1,now())`, batch, binding)
	execEpoch(t, ctx, db, `INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,'cross@fixture.test',decode(repeat('41',32),'hex'),1,'cross')`, account)
	execEpoch(t, ctx, db, `INSERT INTO tsw_operations(id,owner_id,workspace_id,batch_id,operation_type,idempotency_key,request_hash,input_snapshot,correlation_id) VALUES($1,$2,$3,$4,'join','cross-test',decode(repeat('42',32),'hex'),'{}','integration')`, operation, owner, otherSpace, batch)
	execEpoch(t, ctx, db, `INSERT INTO tsw_operation_targets(id,operation_id,target_account_id,ordinal) VALUES($1,$2,$3,1)`, target, operation, account)
	execEpoch(t, ctx, db, `INSERT INTO tsw_batch_memberships(id,batch_id,target_account_id,join_operation_target_id,joined_at) VALUES($1,$2,$3,$4,now())`, membership, batch, account, target)
	execEpoch(t, ctx, db, `INSERT INTO tsw_oauth_assets(id,membership_id) VALUES($1,$2)`, asset, membership)
	key := Key{TargetAccount, account}
	before := epochVersion(t, ctx, db, key)
	var exists bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tsw_batch_memberships m JOIN tsw_oauth_assets a ON a.membership_id=m.id JOIN tsw_delivery_versions d ON d.oauth_asset_id=a.id WHERE m.target_account_id=$1)`, account).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("expected absent delivery")
	}
	lock, versions, err := BeginLocked(ctx, db, []Key{key})
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if versions[0].Version != before {
		t.Fatal("unexpected epoch version")
	}
	done := make(chan error, 1)
	go func() {
		_, err := db.Exec(ctx, `INSERT INTO tsw_delivery_versions(oauth_asset_id,generation,payload,payload_sha256,validated_platform_subject_id,validated_workspace_id) VALUES($1,1,'{}',decode(repeat('43',32),'hex'),'subject',$2)`, asset, otherSpace)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("delivery bypassed account epoch: %v", err)
	case <-time.After(120 * time.Millisecond):
	}
	if err = lock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first delivery did not finish")
	}
	if epochVersion(t, ctx, db, key) != before+1 {
		t.Fatal("first delivery did not advance global account key")
	}
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tsw_batch_memberships m JOIN tsw_oauth_assets a ON a.membership_id=m.id JOIN tsw_delivery_versions d ON d.oauth_asset_id=a.id WHERE m.target_account_id=$1 AND d.validated_workspace_id=$2)`, account, otherSpace).Scan(&exists); err != nil || !exists {
		t.Fatalf("missing cross-space delivery: %v", err)
	}
	// A relationship reassignment changes which account has delivery history.
	next := uuid.New()
	execEpoch(t, ctx, db, `INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,'next@fixture.test',decode(repeat('44',32),'hex'),1,'next')`, next)
	execEpoch(t, ctx, db, `UPDATE tsw_batch_memberships SET target_account_id=$2 WHERE id=$1`, membership, next)
	if epochVersion(t, ctx, db, key) != before+2 || epochVersion(t, ctx, db, Key{TargetAccount, next}) != 2 {
		t.Fatal("account reassignment failed to advance old and new global delivery scopes")
	}
}

func TestEpochDeterministicOrderIntegration(t *testing.T) {
	db, ctx := epochFixture(t)
	one, two := uuid.New(), uuid.New()
	execEpoch(t, ctx, db, `INSERT INTO tsw_standby_child_batches(id,name) VALUES($1,'one'),($2,'two')`, one, two)
	keys := []Key{{StandbyBatch, one}, {StandbyBatch, two}}
	first, versions, err := BeginLocked(ctx, db, keys)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Rollback(ctx)
	if versions[0].Key.ID.String() > versions[1].Key.ID.String() {
		t.Fatal("keys not ordered")
	}
	done := make(chan error, 1)
	go func() {
		tx, _, err := BeginLocked(ctx, db, []Key{keys[1], keys[0]})
		if err == nil {
			err = tx.Commit(ctx)
		}
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("reverse-input lock escaped: %v", err)
	case <-time.After(120 * time.Millisecond):
	}
	if err = first.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("lock order deadlock")
	}
}
