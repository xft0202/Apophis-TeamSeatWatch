-- +goose Up
-- Discovery is a read-only visibility observation, not an operational binding or workspace fact.
CREATE TABLE tsw_mother_discoveries (
    mother_account_id uuid PRIMARY KEY REFERENCES tsw_mother_accounts(id) ON DELETE RESTRICT,
    run_id uuid NOT NULL,
    attempt bigint NOT NULL DEFAULT 1 CHECK (attempt > 0),
    secret_revision bigint NOT NULL CHECK (secret_revision > 0),
    session_generation uuid,
    status text NOT NULL CHECK (status IN ('discovering','discovered','empty','session_expired','missing_credentials','discovery_failed','permission_denied','unavailable')),
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

-- A Personal session is a separate generation, never the mother's password or a Workspace token.
CREATE TABLE tsw_mother_personal_sessions (
    mother_account_id uuid PRIMARY KEY REFERENCES tsw_mother_accounts(id) ON DELETE RESTRICT,
    secret_revision bigint NOT NULL CHECK (secret_revision > 0),
    generation uuid NOT NULL,
    key_version smallint NOT NULL CHECK (key_version > 0),
    nonce bytea NOT NULL CHECK (octet_length(nonce) = 12),
    sealed_session bytea NOT NULL CHECK (octet_length(sealed_session) > 16),
    expires_at timestamptz NOT NULL,
    verified_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE tsw_mother_personal_access (
    mother_account_id uuid PRIMARY KEY REFERENCES tsw_mother_accounts(id) ON DELETE RESTRICT,
    secret_revision bigint NOT NULL CHECK (secret_revision > 0),
    attempt bigint NOT NULL DEFAULT 1 CHECK (attempt > 0),
    status text NOT NULL CHECK (status IN ('verifying','ready','invalid_login','missing_credentials','refresh_failed','unavailable')),
    checked_at timestamptz NOT NULL DEFAULT now()
);
REVOKE ALL ON tsw_mother_personal_sessions FROM PUBLIC;

-- +goose Down
DROP TABLE tsw_mother_personal_access;
DROP TABLE tsw_mother_personal_sessions;
DROP TABLE tsw_mother_workspace_visibility;
DROP TABLE tsw_mother_discoveries;
