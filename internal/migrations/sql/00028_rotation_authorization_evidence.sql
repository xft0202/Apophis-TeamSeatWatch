-- +goose Up
-- Ticket10 production evidence and complete local writer-fence coverage.
-- Remote evidence is still read-only; this migration does not schedule or execute
-- invitations, joins, member removal, delivery, or Hub writes.
ALTER TABLE public.tsw_mother_workspace_visibility
    ADD COLUMN workspace_role text NOT NULL DEFAULT 'unknown'
    CONSTRAINT tsw_mother_workspace_visibility_role_ck CHECK (workspace_role IN ('owner','admin','member','unknown'));

ALTER TABLE public.tsw_workspace_verifications
    ADD COLUMN seat_type_counts jsonb NOT NULL DEFAULT '{}'::jsonb
    CONSTRAINT tsw_workspace_verifications_seat_types_ck CHECK (jsonb_typeof(seat_type_counts)='object');
ALTER TABLE public.tsw_workspace_verification_entries
    ADD COLUMN seat_type text
    CONSTRAINT tsw_workspace_verification_entries_seat_type_ck
    CHECK (seat_type IS NULL OR seat_type IN ('default','usage_based','automation','prolite'));

CREATE TABLE public.tsw_rotation_usage_ledger (
    target_account_id uuid NOT NULL REFERENCES public.tsw_target_accounts(id) ON DELETE RESTRICT,
    workspace_id uuid NOT NULL REFERENCES public.tsw_workspaces(id) ON DELETE RESTRICT,
    usage_state text NOT NULL CHECK (usage_state IN ('unknown','never_used','used')),
    ever_used boolean NOT NULL DEFAULT false,
    evidence_source text NOT NULL CHECK (length(evidence_source) BETWEEN 1 AND 128),
    evidence_id text NOT NULL CHECK (length(evidence_id)=64),
    observed_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (target_account_id,workspace_id),
    CHECK (expires_at > observed_at AND expires_at <= observed_at + interval '5 minutes'),
    CHECK ((usage_state='used') = ever_used)
);
REVOKE ALL ON public.tsw_rotation_usage_ledger FROM PUBLIC;

-- +goose StatementBegin
CREATE FUNCTION public.tsw_rotation_usage_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
    IF NEW.target_account_id<>OLD.target_account_id OR NEW.workspace_id<>OLD.workspace_id OR NEW.version<>OLD.version+1 THEN
        RAISE EXCEPTION 'rotation usage evidence requires one immutable-scope version step';
    END IF;
    IF OLD.ever_used AND NOT NEW.ever_used THEN
        RAISE EXCEPTION 'rotation ever_used evidence is monotonic';
    END IF;
    IF OLD.usage_state='used' AND NEW.usage_state<>'used' THEN
        RAISE EXCEPTION 'rotation used evidence cannot be downgraded';
    END IF;
    IF OLD.usage_state='never_used' AND NEW.usage_state='unknown' THEN
        RAISE EXCEPTION 'rotation never_used evidence cannot be downgraded';
    END IF;
    NEW.updated_at=now();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER tsw_rotation_usage_guard BEFORE UPDATE ON public.tsw_rotation_usage_ledger
FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_usage_guard();

CREATE TABLE public.tsw_rotation_global_protections (
    target_account_id uuid PRIMARY KEY REFERENCES public.tsw_target_accounts(id) ON DELETE RESTRICT,
    status text NOT NULL CHECK (status IN ('none','suspected_sold','sale_reserved','delivery_pending','delivered','canceled_retired')),
    evidence_source text NOT NULL CHECK (length(evidence_source) BETWEEN 1 AND 128),
    evidence_id text NOT NULL CHECK (length(evidence_id)=64),
    observed_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);
REVOKE ALL ON public.tsw_rotation_global_protections FROM PUBLIC;

-- +goose StatementBegin
CREATE FUNCTION public.tsw_rotation_protection_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
    IF TG_OP='DELETE' THEN
        RAISE EXCEPTION 'rotation global protection cannot be deleted';
    END IF;
    IF NEW.target_account_id<>OLD.target_account_id OR NEW.version<>OLD.version+1 THEN
        RAISE EXCEPTION 'rotation protection requires one immutable-account version step';
    END IF;
    IF OLD.status IN ('delivered','canceled_retired') AND NEW.status<>OLD.status THEN
        RAISE EXCEPTION 'permanent rotation protection cannot change';
    END IF;
    NEW.updated_at=now();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER tsw_rotation_protection_guard BEFORE UPDATE OR DELETE ON public.tsw_rotation_global_protections
FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_protection_guard();

ALTER TABLE public.tsw_rotation_epochs DROP CONSTRAINT tsw_rotation_epochs_kind_check;
ALTER TABLE public.tsw_rotation_epochs ADD CONSTRAINT tsw_rotation_epochs_kind_check
CHECK (kind IN ('owner','workspace','mother','target_account','standby_batch','destination','target_identity','standby_membership'));
INSERT INTO public.tsw_rotation_epochs(kind,id) VALUES
    ('target_identity','00000000-0000-0000-0000-000000000001'),
    ('standby_membership','00000000-0000-0000-0000-000000000002');

-- +goose StatementBegin
CREATE FUNCTION public.tsw_rotation_epoch_singleton() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
    PERFORM public.tsw_rotation_epoch_advance(TG_ARGV[0],TG_ARGV[1]::uuid);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER tsw_rotation_epoch_identity AFTER INSERT OR UPDATE OR DELETE ON public.tsw_target_accounts
FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_epoch_singleton('target_identity','00000000-0000-0000-0000-000000000001');
CREATE TRIGGER tsw_rotation_epoch_standby_target AFTER INSERT OR UPDATE OR DELETE ON public.tsw_standby_child_memberships
FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_epoch_fact('target_account','target_account_id');
CREATE TRIGGER tsw_rotation_epoch_standby_batch AFTER INSERT OR UPDATE OR DELETE ON public.tsw_standby_child_memberships
FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_epoch_fact('standby_batch','batch_id');
CREATE TRIGGER tsw_rotation_epoch_standby_global AFTER INSERT OR UPDATE OR DELETE ON public.tsw_standby_child_memberships
FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_epoch_singleton('standby_membership','00000000-0000-0000-0000-000000000002');
CREATE TRIGGER tsw_rotation_epoch_usage AFTER INSERT OR UPDATE OR DELETE ON public.tsw_rotation_usage_ledger
FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_epoch_fact('target_account','target_account_id');
CREATE TRIGGER tsw_rotation_epoch_protection AFTER INSERT OR UPDATE OR DELETE ON public.tsw_rotation_global_protections
FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_epoch_fact('target_account','target_account_id');

-- Preview rows are outputs, not source facts. Their INSERT or authorization
-- transition must not invalidate the source epochs captured by that preview.
DROP TRIGGER tsw_rotation_epoch_fact ON public.tsw_expiry_rotation_previews;

ALTER TABLE public.tsw_expiry_rotation_previews
    ADD COLUMN epoch_versions jsonb
    CONSTRAINT tsw_expiry_rotation_epoch_versions_ck CHECK (epoch_versions IS NULL OR jsonb_typeof(epoch_versions)='object');

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.tsw_expiry_rotation_preview_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
 IF (NEW.id,NEW.owner_id,NEW.draft_id,NEW.draft_version,NEW.workspace_id,NEW.verification_id,NEW.facts,NEW.digest,NEW.expires_at,NEW.created_at)
    IS DISTINCT FROM (OLD.id,OLD.owner_id,OLD.draft_id,OLD.draft_version,OLD.workspace_id,OLD.verification_id,OLD.facts,OLD.digest,OLD.expires_at,OLD.created_at) THEN
   RAISE EXCEPTION 'expiry rotation preview facts are immutable';
 END IF;
 IF (OLD.status='ready' AND NEW.status='authorized' AND OLD.idempotency_key IS NULL AND NEW.idempotency_key IS NOT NULL
     AND NEW.authorized_by IS NOT NULL AND NEW.authorized_session IS NOT NULL AND NEW.authorized_at IS NOT NULL AND NEW.revoked_at IS NULL
     AND NEW.authorization_digest IS NOT NULL AND jsonb_array_length(NEW.assignments)>0
     AND NEW.epoch_versions IS NOT NULL AND NEW.epoch_versions<>'{}'::jsonb)
    OR (OLD.status='authorized' AND NEW.status='revoked' AND NEW.idempotency_key=OLD.idempotency_key
     AND NEW.authorized_by=OLD.authorized_by AND NEW.authorized_session=OLD.authorized_session AND NEW.authorized_at=OLD.authorized_at
     AND NEW.assignments=OLD.assignments AND NEW.authorization_digest=OLD.authorization_digest
     AND NEW.epoch_versions=OLD.epoch_versions AND NEW.revoked_at IS NOT NULL AND NEW.revoked_by IS NOT NULL) THEN RETURN NEW; END IF;
 RAISE EXCEPTION 'expiry rotation authorization transition denied';
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.tsw_expiry_rotation_preview_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
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
ALTER TABLE public.tsw_expiry_rotation_previews DROP COLUMN epoch_versions;
CREATE TRIGGER tsw_rotation_epoch_fact AFTER INSERT OR UPDATE OR DELETE ON public.tsw_expiry_rotation_previews
FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_epoch_fact('workspace','workspace_id');
DROP TRIGGER tsw_rotation_epoch_protection ON public.tsw_rotation_global_protections;
DROP TRIGGER tsw_rotation_epoch_usage ON public.tsw_rotation_usage_ledger;
DROP TRIGGER tsw_rotation_epoch_standby_global ON public.tsw_standby_child_memberships;
DROP TRIGGER tsw_rotation_epoch_standby_batch ON public.tsw_standby_child_memberships;
DROP TRIGGER tsw_rotation_epoch_standby_target ON public.tsw_standby_child_memberships;
DROP TRIGGER tsw_rotation_epoch_identity ON public.tsw_target_accounts;
DROP FUNCTION public.tsw_rotation_epoch_singleton();
DELETE FROM public.tsw_rotation_epochs WHERE kind IN ('target_identity','standby_membership');
ALTER TABLE public.tsw_rotation_epochs DROP CONSTRAINT tsw_rotation_epochs_kind_check;
ALTER TABLE public.tsw_rotation_epochs ADD CONSTRAINT tsw_rotation_epochs_kind_check
CHECK (kind IN ('owner','workspace','mother','target_account','standby_batch','destination'));
DROP TRIGGER tsw_rotation_protection_guard ON public.tsw_rotation_global_protections;
DROP FUNCTION public.tsw_rotation_protection_guard();
DROP TABLE public.tsw_rotation_global_protections;
DROP TRIGGER tsw_rotation_usage_guard ON public.tsw_rotation_usage_ledger;
DROP FUNCTION public.tsw_rotation_usage_guard();
DROP TABLE public.tsw_rotation_usage_ledger;
ALTER TABLE public.tsw_workspace_verification_entries DROP COLUMN seat_type;
ALTER TABLE public.tsw_workspace_verifications DROP COLUMN seat_type_counts;
ALTER TABLE public.tsw_mother_workspace_visibility DROP COLUMN workspace_role;
