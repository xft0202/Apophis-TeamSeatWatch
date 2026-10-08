-- +goose Up
-- Standby grouping is independent of workspace-bound operational batches and memberships.
CREATE TABLE tsw_standby_child_batches (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 120),
 version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE tsw_standby_child_memberships (
 target_account_id uuid PRIMARY KEY REFERENCES tsw_target_accounts(id) ON DELETE RESTRICT,
 batch_id uuid REFERENCES tsw_standby_child_batches(id) ON DELETE RESTRICT,
 version bigint NOT NULL DEFAULT 1 CHECK (version > 0)
);
CREATE INDEX tsw_standby_child_memberships_batch_idx ON tsw_standby_child_memberships(batch_id);
CREATE TABLE tsw_standby_child_history (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 target_account_id uuid NOT NULL REFERENCES tsw_target_accounts(id) ON DELETE RESTRICT,
 previous_batch_id uuid REFERENCES tsw_standby_child_batches(id) ON DELETE RESTRICT,
 batch_id uuid REFERENCES tsw_standby_child_batches(id) ON DELETE RESTRICT,
 owner_id uuid NOT NULL,
 changed_at timestamptz NOT NULL DEFAULT now(),
 CHECK (previous_batch_id IS DISTINCT FROM batch_id)
);
-- +goose Down
DROP TABLE tsw_standby_child_history;
DROP TABLE tsw_standby_child_memberships;
DROP TABLE tsw_standby_child_batches;
