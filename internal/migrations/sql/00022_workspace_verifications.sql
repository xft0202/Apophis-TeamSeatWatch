-- +goose Up
-- Selected-mother verification is independent of operational bindings and per-mother visibility.
-- Every attempt, including a failed or partial read, is retained without overwriting another space.
-- This is the single final 00022 on an isolated, non-deployed Ticket07 branch;
-- the partial 00022 was never applied outside disposable integration databases.
CREATE TABLE tsw_selected_workspace_tokens (
    mother_account_id uuid NOT NULL REFERENCES tsw_mother_accounts(id) ON DELETE RESTRICT,
    workspace_id uuid NOT NULL REFERENCES tsw_workspaces(id) ON DELETE RESTRICT,
    discovery_run_id uuid NOT NULL,
    session_generation uuid NOT NULL,
    secret_revision bigint NOT NULL CHECK (secret_revision > 0),
    attempt bigint NOT NULL DEFAULT 1 CHECK (attempt > 0),
    exchange_id uuid NOT NULL,
    status text NOT NULL CHECK (status IN ('exchanging','ready','failed')),
    key_version smallint CHECK (key_version IS NULL OR key_version > 0),
    nonce bytea CHECK (nonce IS NULL OR octet_length(nonce) = 12),
    sealed_access bytea CHECK (sealed_access IS NULL OR octet_length(sealed_access) > 16),
    expires_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (mother_account_id,workspace_id),
    CHECK ((status='ready') = (key_version IS NOT NULL AND nonce IS NOT NULL AND sealed_access IS NOT NULL AND expires_at IS NOT NULL)),
    CHECK (status='ready' OR (key_version IS NULL AND nonce IS NULL AND sealed_access IS NULL AND expires_at IS NULL))
);
CREATE INDEX tsw_selected_workspace_tokens_expiry_idx ON tsw_selected_workspace_tokens(expires_at);
REVOKE ALL ON tsw_selected_workspace_tokens FROM PUBLIC;

CREATE TABLE tsw_workspace_verifications (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES tsw_workspaces(id) ON DELETE RESTRICT,
    mother_account_id uuid NOT NULL REFERENCES tsw_mother_accounts(id) ON DELETE RESTRICT,
    discovery_run_id uuid NOT NULL,
    session_generation uuid NOT NULL,
    secret_revision bigint NOT NULL CHECK (secret_revision > 0),
    token_attempt bigint NOT NULL CHECK (token_attempt > 0),
    token_exchange_id uuid NOT NULL,
    source text NOT NULL CHECK (source IN ('injected_platform_reader','official_readonly')),
    outcome text NOT NULL CHECK (outcome IN ('verifying','verified','partial','failed','permission_denied')),
    sources jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(sources) = 'array'),
    permission text NOT NULL CHECK (permission IN ('manage','read','denied','unknown')),
    completeness text NOT NULL CHECK (completeness IN ('complete','partial','unknown')),
    observed_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    active_until timestamptz,
    seat_limit integer CHECK (seat_limit IS NULL OR seat_limit >= 0),
    member_count integer CHECK (member_count IS NULL OR member_count >= 0),
    pending_invite_count integer CHECK (pending_invite_count IS NULL OR pending_invite_count >= 0),
    CHECK (expires_at > observed_at AND expires_at <= observed_at + interval '7 days'),
    CHECK (outcome <> 'verified' OR (completeness = 'complete' AND permission IN ('manage','read') AND active_until IS NOT NULL AND seat_limit IS NOT NULL AND member_count IS NOT NULL AND pending_invite_count IS NOT NULL)),
    CHECK (outcome = 'verified' OR (active_until IS NULL AND seat_limit IS NULL AND member_count IS NULL AND pending_invite_count IS NULL))
);
CREATE INDEX tsw_workspace_verifications_latest_idx ON tsw_workspace_verifications(workspace_id,mother_account_id,discovery_run_id,session_generation,token_exchange_id,id DESC);
CREATE TABLE tsw_workspace_verification_entries (
    verification_id bigint NOT NULL REFERENCES tsw_workspace_verifications(id) ON DELETE RESTRICT,
    kind text NOT NULL CHECK (kind IN ('member','pending_invite')),
    identifier text NOT NULL CHECK (length(identifier) BETWEEN 1 AND 254),
    identifier_hmac bytea NOT NULL CHECK (octet_length(identifier_hmac) = 32),
    identifier_key_version smallint NOT NULL CHECK (identifier_key_version > 0),
    status text NOT NULL CHECK (length(status) BETWEEN 1 AND 64),
    role text CHECK (role IS NULL OR length(role) BETWEEN 1 AND 64),
    platform_member_id text,
    PRIMARY KEY (verification_id,kind,identifier_key_version,identifier_hmac),
    CHECK ((kind = 'member' AND platform_member_id IS NOT NULL) OR (kind = 'pending_invite' AND platform_member_id IS NULL))
);
REVOKE ALL ON tsw_workspace_verifications,tsw_workspace_verification_entries FROM PUBLIC;
-- +goose StatementBegin
CREATE FUNCTION tsw_workspace_verification_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND TG_TABLE_NAME = 'tsw_workspace_verifications' AND OLD.outcome = 'verifying' AND
       NEW.outcome IN ('verified','partial','failed','permission_denied') AND
       (NEW.id,NEW.workspace_id,NEW.mother_account_id,NEW.discovery_run_id,NEW.session_generation,NEW.secret_revision,NEW.token_attempt,NEW.token_exchange_id,NEW.source) =
       (OLD.id,OLD.workspace_id,OLD.mother_account_id,OLD.discovery_run_id,OLD.session_generation,OLD.secret_revision,OLD.token_attempt,OLD.token_exchange_id,OLD.source) THEN RETURN NEW; END IF;
    IF TG_OP = 'DELETE' AND TG_TABLE_NAME = 'tsw_workspace_verifications' AND OLD.expires_at <= now() THEN RETURN OLD; END IF;
    IF TG_OP = 'DELETE' AND TG_TABLE_NAME = 'tsw_workspace_verification_entries' AND
       EXISTS (SELECT 1 FROM tsw_workspace_verifications WHERE id = OLD.verification_id AND expires_at <= now()) THEN RETURN OLD; END IF;
    RAISE EXCEPTION 'workspace verification is append-only until expiry';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER tsw_workspace_verifications_immutable BEFORE UPDATE OR DELETE ON tsw_workspace_verifications FOR EACH ROW EXECUTE FUNCTION tsw_workspace_verification_immutable();
CREATE TRIGGER tsw_workspace_verification_entries_immutable BEFORE UPDATE OR DELETE ON tsw_workspace_verification_entries FOR EACH ROW EXECUTE FUNCTION tsw_workspace_verification_immutable();

-- +goose Down
DROP TABLE tsw_workspace_verification_entries;
DROP TABLE tsw_workspace_verifications;
DROP TABLE tsw_selected_workspace_tokens;
DROP FUNCTION tsw_workspace_verification_immutable();
