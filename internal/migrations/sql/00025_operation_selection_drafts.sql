-- +goose Up
-- One resumable, selection-only draft per Owner. There is deliberately no execution state or route.
CREATE TABLE tsw_operation_selection_drafts (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 owner_id uuid NOT NULL UNIQUE REFERENCES tsw_owners(id) ON DELETE RESTRICT,
 version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
 step text NOT NULL DEFAULT 'mother' CHECK (step IN ('mother','workspace','children','destination','complete')),
 mother_account_id uuid REFERENCES tsw_mother_accounts(id) ON DELETE RESTRICT,
 mother_revision bigint,
 workspace_id uuid REFERENCES tsw_workspaces(id) ON DELETE RESTRICT,
 visibility_run_id uuid,
 session_generation uuid,
 -- Retention deletes expired verification facts. Keep only their immutable ID:
 -- currentness checks must fail closed once this fact no longer exists.
 verification_id bigint,
 batch_id uuid REFERENCES tsw_standby_child_batches(id) ON DELETE RESTRICT,
 batch_version bigint,
 children jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(children)='array' AND jsonb_array_length(children)<=10000),
 destination_id uuid REFERENCES tsw_delivery_destinations(id) ON DELETE RESTRICT,
 destination_revision bigint,
 updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK ((mother_account_id IS NULL) = (mother_revision IS NULL)),
 CHECK ((workspace_id IS NULL) = (visibility_run_id IS NULL) AND (workspace_id IS NULL) = (session_generation IS NULL) AND (workspace_id IS NULL) = (verification_id IS NULL)),
 CHECK ((batch_id IS NULL) = (batch_version IS NULL)),
 CHECK ((destination_id IS NULL) = (destination_revision IS NULL))
);
REVOKE ALL ON tsw_operation_selection_drafts FROM PUBLIC;
-- +goose Down
DROP TABLE tsw_operation_selection_drafts;
