-- +goose Up
-- Discovery is a read-only visibility observation, not an operational binding or workspace fact.
CREATE TABLE tsw_mother_discoveries (
    mother_account_id uuid PRIMARY KEY REFERENCES tsw_mother_accounts(id) ON DELETE RESTRICT,
    run_id uuid NOT NULL,
    secret_revision bigint NOT NULL CHECK (secret_revision > 0),
    status text NOT NULL CHECK (status IN ('discovered','empty','invalid_login','missing_credentials','discovery_failed','permission_denied','unavailable')),
    observed_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE tsw_mother_workspace_visibility (
    mother_account_id uuid NOT NULL REFERENCES tsw_mother_accounts(id) ON DELETE RESTRICT,
    workspace_id uuid NOT NULL REFERENCES tsw_workspaces(id) ON DELETE RESTRICT,
    run_id uuid NOT NULL,
    access_status text NOT NULL CHECK (access_status IN ('readable','permission_denied','unknown')),
    observed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (mother_account_id,workspace_id)
);
CREATE INDEX tsw_mother_workspace_visibility_workspace_idx ON tsw_mother_workspace_visibility(workspace_id);

-- +goose Down
DROP TABLE tsw_mother_workspace_visibility;
DROP TABLE tsw_mother_discoveries;
