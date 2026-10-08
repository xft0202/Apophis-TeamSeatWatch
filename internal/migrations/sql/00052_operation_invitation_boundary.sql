-- +goose Up
-- Invitation is a durable fact independent of later workspace OAuth login.
ALTER TABLE tsw_operation_targets ADD COLUMN invitation_confirmed_at timestamptz;
ALTER TABLE tsw_operation_targets DROP CONSTRAINT tsw_operation_targets_status_ck;
ALTER TABLE tsw_operation_targets ADD CONSTRAINT tsw_operation_targets_status_ck CHECK (status IN ('queued','running','invited','succeeded','failed','blocked','unknown'));
ALTER TABLE tsw_operation_targets ADD CONSTRAINT tsw_operation_targets_invited_ck CHECK (status <> 'invited' OR invitation_confirmed_at IS NOT NULL);
ALTER TABLE tsw_operations DROP CONSTRAINT tsw_operations_status_ck;
ALTER TABLE tsw_operations ADD CONSTRAINT tsw_operations_status_ck CHECK (status IN ('queued','running','awaiting_login','succeeded','failed','blocked'));
DROP INDEX tsw_operations_workspace_active_uq;
CREATE UNIQUE INDEX tsw_operations_workspace_active_uq ON tsw_operations(workspace_id)
 WHERE operation_type='join' AND status IN ('queued','running','awaiting_login','blocked');
-- Preserve established premium members and recipient stages already authorized
-- after a confirmed premium invitation. Do not reset side-effect markers.
UPDATE tsw_operation_targets t SET invitation_confirmed_at=COALESCE(t.completed_at,t.platform_request_started_at,t.updated_at)
 FROM tsw_operations op WHERE op.id=t.operation_id AND op.input_snapshot->>'seat_type'='prolite'
 AND (t.status='succeeded' OR t.platform_request_stage IN ('request_join','accept_join'));
-- Workspace OAuth tasks share the explicitly chosen step 3 limit.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION tsw_assign_task_concurrency() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.operation_target_id IS NOT NULL THEN
  SELECT CASE WHEN b.login_started_at IS NULL THEN 'operation:'||op.id::text ELSE 'login:'||b.id::text END,
    CASE WHEN b.login_started_at IS NULL THEN op.task_concurrency ELSE b.login_task_concurrency END
   INTO NEW.concurrency_key,NEW.concurrency_limit
   FROM tsw_operation_targets t JOIN tsw_operations op ON op.id=t.operation_id JOIN tsw_batches b ON b.id=op.batch_id WHERE t.id=NEW.operation_target_id;
 ELSIF NEW.membership_id IS NOT NULL AND NEW.task_type IN ('oauth_generate','oauth_probe','oauth_reclaim') THEN
  SELECT 'login:'||b.id::text,b.login_task_concurrency INTO NEW.concurrency_key,NEW.concurrency_limit
   FROM tsw_batch_memberships m JOIN tsw_batches b ON b.id=m.batch_id WHERE m.id=NEW.membership_id;
 END IF;
 NEW.concurrency_key:=COALESCE(NEW.concurrency_key,'');
 NEW.concurrency_limit:=COALESCE(NEW.concurrency_limit,1);
 RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- This release changes a persisted authorization boundary; rollbacks require
-- reviewing pending invitations rather than silently discarding that state.
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Review pending invitation authorization before rollback'; END $$;
-- +goose StatementEnd
