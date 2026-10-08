-- +goose Up
-- No task or platform write is wired to this authorization. Snapshots are immutable;
-- revocation is the only permitted state transition before any remote mutation.
CREATE TABLE tsw_expiry_rotation_previews (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 owner_id uuid NOT NULL REFERENCES tsw_owners(id) ON DELETE RESTRICT,
 draft_id uuid NOT NULL REFERENCES tsw_operation_selection_drafts(id) ON DELETE RESTRICT,
 draft_version bigint NOT NULL CHECK (draft_version > 0),
 workspace_id uuid NOT NULL REFERENCES tsw_workspaces(id) ON DELETE RESTRICT,
 verification_id bigint NOT NULL,
 facts jsonb NOT NULL CHECK (jsonb_typeof(facts)='object'),
 digest text NOT NULL CHECK (length(digest)=64),
 status text NOT NULL CHECK (status IN ('ready','pending_permission','facts_incomplete','not_expired','needs_verification','authorized','revoked')),
 assignments jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(assignments)='array'),
 authorization_digest text CHECK (authorization_digest IS NULL OR length(authorization_digest)=64),
 idempotency_key uuid,
 authorized_by uuid REFERENCES tsw_owners(id) ON DELETE RESTRICT,
 authorized_session text,
 authorized_at timestamptz,
 revoked_by uuid REFERENCES tsw_owners(id) ON DELETE RESTRICT,
 revoked_at timestamptz,
 expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(owner_id,idempotency_key),
 CHECK ((status IN ('authorized','revoked')) = (authorized_at IS NOT NULL)),
 CHECK ((status IN ('authorized','revoked')) = (authorization_digest IS NOT NULL)),
 CHECK ((status IN ('authorized','revoked')) OR assignments='[]'::jsonb),
 CHECK ((status='revoked') = (revoked_at IS NOT NULL))
);
CREATE INDEX tsw_expiry_rotation_previews_owner_idx ON tsw_expiry_rotation_previews(owner_id,created_at DESC);
REVOKE ALL ON tsw_expiry_rotation_previews FROM PUBLIC;
-- +goose StatementBegin
CREATE FUNCTION tsw_expiry_rotation_preview_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.id,NEW.owner_id,NEW.draft_id,NEW.draft_version,NEW.workspace_id,NEW.verification_id,NEW.facts,NEW.digest,NEW.expires_at,NEW.created_at)
    IS DISTINCT FROM (OLD.id,OLD.owner_id,OLD.draft_id,OLD.draft_version,OLD.workspace_id,OLD.verification_id,OLD.facts,OLD.digest,OLD.expires_at,OLD.created_at) THEN
   RAISE EXCEPTION 'expiry rotation preview facts are immutable';
 END IF;
 IF (OLD.status='ready' AND NEW.status='authorized' AND OLD.idempotency_key IS NULL AND NEW.idempotency_key IS NOT NULL
     AND NEW.authorized_by IS NOT NULL AND NEW.authorized_session IS NOT NULL AND NEW.authorized_at IS NOT NULL AND NEW.revoked_at IS NULL
     AND NEW.authorization_digest IS NOT NULL AND jsonb_array_length(NEW.assignments)>0)
    OR (OLD.status='authorized' AND NEW.status='revoked' AND NEW.idempotency_key=OLD.idempotency_key
     AND NEW.authorized_by=OLD.authorized_by AND NEW.authorized_session=OLD.authorized_session AND NEW.authorized_at=OLD.authorized_at
     AND NEW.assignments=OLD.assignments AND NEW.authorization_digest=OLD.authorization_digest
     AND NEW.revoked_at IS NOT NULL AND NEW.revoked_by IS NOT NULL) THEN RETURN NEW; END IF;
 RAISE EXCEPTION 'expiry rotation authorization transition denied';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER tsw_expiry_rotation_preview_guard BEFORE UPDATE ON tsw_expiry_rotation_previews FOR EACH ROW EXECUTE FUNCTION tsw_expiry_rotation_preview_guard();
-- +goose Down
DROP TABLE tsw_expiry_rotation_previews;
DROP FUNCTION tsw_expiry_rotation_preview_guard();
