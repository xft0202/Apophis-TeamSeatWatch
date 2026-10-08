//go:build integration

package writerfence

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// The standby editor takes account then batch parent locks before it mutates
// membership. The membership trigger joins both epochs; a confirmation that
// already owns them serializes the writer without taking any fact-row lock.
func TestStandbyEditorEpochLockOrderIntegration(t *testing.T) {
	db, ctx := epochFixture(t)
	account, batch := uuid.New(), uuid.New()
	execEpoch(t, ctx, db, `INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) VALUES($1,'order@fixture.test',decode(repeat('61',32),'hex'),1,'order')`, account)
	execEpoch(t, ctx, db, `INSERT INTO tsw_standby_child_batches(id,name) VALUES($1,'order')`, batch)
	fence, versions, err := BeginLocked(ctx, db, []Key{{TargetAccount, account}, {StandbyBatch, batch}})
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Rollback(ctx)
	if len(versions) != 2 || versions[0].Key.Kind != StandbyBatch {
		t.Fatal("epoch keys not sorted")
	}
	writer, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(ctx)
	if _, err = writer.Exec(ctx, `SELECT id FROM tsw_target_accounts WHERE id=$1 FOR UPDATE`, account); err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Exec(ctx, `SELECT id FROM tsw_standby_child_batches WHERE id=$1 FOR UPDATE`, batch); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := writer.Exec(ctx, `INSERT INTO tsw_standby_child_memberships(target_account_id,batch_id) VALUES($1,$2)`, account, batch)
		if err == nil {
			_, err = writer.Exec(ctx, `UPDATE tsw_standby_child_batches SET version=version+1 WHERE id=$1`, batch)
		}
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("writer bypassed batch epoch: %v", err)
	case <-time.After(120 * time.Millisecond):
	}
	if err = fence.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("standby editor / epoch lock deadlock")
	}
	if err = writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if epochVersion(t, ctx, db, Key{StandbyBatch, batch}) != 3 {
		t.Fatal("batch epoch not advanced")
	}
}
