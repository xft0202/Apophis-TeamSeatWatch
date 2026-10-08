-- +goose Up
ALTER TABLE tsw_proxy_pool_sources DROP CONSTRAINT tsw_proxy_pool_sources_kind_check;
ALTER TABLE tsw_proxy_pool_sources ADD CONSTRAINT tsw_proxy_pool_sources_kind_check CHECK (kind IN ('subscription','cliproxy','b2proxy','proxy1024'));
ALTER TABLE tsw_proxy_pool_sources RENAME COLUMN url_key_version TO secret_key_version;
ALTER TABLE tsw_proxy_pool_sources RENAME COLUMN url_nonce TO secret_nonce;
ALTER TABLE tsw_proxy_pool_sources RENAME COLUMN url_ciphertext TO secret_ciphertext;
ALTER TABLE tsw_proxy_pool_sources ADD COLUMN config JSONB NOT NULL DEFAULT '{"updateSeconds":300}';
ALTER TABLE tsw_proxy_pool_sources ADD COLUMN retry_at TIMESTAMPTZ;
CREATE UNIQUE INDEX tsw_proxy_pool_one_enabled_source ON tsw_proxy_pool_sources(enabled) WHERE enabled=true;

ALTER TABLE tsw_proxy_pool_nodes ADD COLUMN session_key TEXT NOT NULL DEFAULT '';
ALTER TABLE tsw_proxy_pool_nodes ADD COLUMN region TEXT NOT NULL DEFAULT '';
ALTER TABLE tsw_proxy_pool_nodes ADD COLUMN stable_until TIMESTAMPTZ;
ALTER TABLE tsw_proxy_pool_nodes ADD COLUMN session_type TEXT NOT NULL DEFAULT 'sticky' CHECK (session_type IN ('sticky','rotating'));
ALTER TABLE tsw_proxy_pool_nodes ADD COLUMN retired BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE tsw_proxy_pool_nodes ADD COLUMN diagnostics JSONB NOT NULL DEFAULT '[]';
CREATE INDEX tsw_proxy_pool_nodes_lifetime_idx ON tsw_proxy_pool_nodes(stable_until) WHERE stable_until IS NOT NULL;

ALTER TABLE tsw_proxy_pool_settings ADD COLUMN refill_requested INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tsw_proxy_pool_settings ADD COLUMN refill_generated INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tsw_proxy_pool_settings ADD COLUMN refill_succeeded INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tsw_proxy_pool_settings ADD COLUMN last_refill_at TIMESTAMPTZ;

ALTER TABLE tsw_personal_probe_batches ADD COLUMN task_concurrency INTEGER NOT NULL DEFAULT 1 CHECK (task_concurrency BETWEEN 1 AND 100);
ALTER TABLE tsw_operations ADD COLUMN task_concurrency INTEGER NOT NULL DEFAULT 1 CHECK (task_concurrency BETWEEN 1 AND 100);
ALTER TABLE tsw_batches ADD COLUMN login_task_concurrency INTEGER NOT NULL DEFAULT 1 CHECK (login_task_concurrency BETWEEN 1 AND 100);
ALTER TABLE tsw_tasks ADD COLUMN concurrency_key TEXT NOT NULL DEFAULT '';
ALTER TABLE tsw_tasks ADD COLUMN concurrency_limit INTEGER NOT NULL DEFAULT 1 CHECK (concurrency_limit BETWEEN 1 AND 100);
CREATE INDEX tsw_tasks_concurrency_idx ON tsw_tasks(concurrency_key) WHERE status='running';
-- +goose StatementBegin
CREATE FUNCTION tsw_assign_task_concurrency() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.operation_target_id IS NOT NULL THEN
    SELECT 'operation:'||op.id::text,op.task_concurrency INTO NEW.concurrency_key,NEW.concurrency_limit
      FROM tsw_operation_targets target JOIN tsw_operations op ON op.id=target.operation_id WHERE target.id=NEW.operation_target_id;
  ELSIF NEW.membership_id IS NOT NULL AND NEW.task_type IN ('oauth_generate','oauth_probe','oauth_reclaim') THEN
    SELECT 'login:'||batch.id::text,batch.login_task_concurrency INTO NEW.concurrency_key,NEW.concurrency_limit
      FROM tsw_batch_memberships membership JOIN tsw_batches batch ON batch.id=membership.batch_id WHERE membership.id=NEW.membership_id;
  END IF;
  NEW.concurrency_key:=COALESCE(NEW.concurrency_key,'');
  NEW.concurrency_limit:=COALESCE(NEW.concurrency_limit,1);
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER tsw_task_concurrency_before_insert BEFORE INSERT ON tsw_tasks FOR EACH ROW EXECUTE FUNCTION tsw_assign_task_concurrency();
UPDATE tsw_tasks t SET concurrency_key='operation:'||op.id::text,concurrency_limit=op.task_concurrency FROM tsw_operation_targets target JOIN tsw_operations op ON op.id=target.operation_id WHERE t.operation_target_id=target.id;
UPDATE tsw_tasks t SET concurrency_key='login:'||b.id::text,concurrency_limit=b.login_task_concurrency FROM tsw_batch_memberships m JOIN tsw_batches b ON b.id=m.batch_id WHERE t.membership_id=m.id AND t.operation_target_id IS NULL AND t.task_type IN ('oauth_generate','oauth_probe','oauth_reclaim');

-- +goose Down
DROP TRIGGER tsw_task_concurrency_before_insert ON tsw_tasks;
DROP FUNCTION tsw_assign_task_concurrency();
DROP INDEX tsw_tasks_concurrency_idx;
ALTER TABLE tsw_tasks DROP COLUMN concurrency_key,DROP COLUMN concurrency_limit;
ALTER TABLE tsw_batches DROP COLUMN login_task_concurrency;
ALTER TABLE tsw_operations DROP COLUMN task_concurrency;
ALTER TABLE tsw_personal_probe_batches DROP COLUMN task_concurrency;
ALTER TABLE tsw_proxy_pool_settings DROP COLUMN refill_requested, DROP COLUMN refill_generated, DROP COLUMN refill_succeeded, DROP COLUMN last_refill_at;
DROP INDEX tsw_proxy_pool_nodes_lifetime_idx;
ALTER TABLE tsw_proxy_pool_nodes DROP COLUMN session_key, DROP COLUMN region, DROP COLUMN stable_until, DROP COLUMN session_type, DROP COLUMN retired, DROP COLUMN diagnostics;
DROP INDEX tsw_proxy_pool_one_enabled_source;
ALTER TABLE tsw_proxy_pool_sources DROP COLUMN config, DROP COLUMN retry_at;
ALTER TABLE tsw_proxy_pool_sources RENAME COLUMN secret_key_version TO url_key_version;
ALTER TABLE tsw_proxy_pool_sources RENAME COLUMN secret_nonce TO url_nonce;
ALTER TABLE tsw_proxy_pool_sources RENAME COLUMN secret_ciphertext TO url_ciphertext;
-- Restoring a subscription-only schema requires manually removing supplier configs first.
ALTER TABLE tsw_proxy_pool_sources DROP CONSTRAINT tsw_proxy_pool_sources_kind_check;
ALTER TABLE tsw_proxy_pool_sources ADD CONSTRAINT tsw_proxy_pool_sources_kind_check CHECK (kind='subscription');
