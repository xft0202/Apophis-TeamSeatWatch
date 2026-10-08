-- +goose Up
-- Ticket 11 outputs do not rewrite Ticket 10 source facts or frozen epochs.
CREATE TABLE public.tsw_rotation_removals (
 preview_id uuid PRIMARY KEY REFERENCES public.tsw_expiry_rotation_previews(id) ON DELETE RESTRICT,
 workspace_id uuid NOT NULL REFERENCES public.tsw_workspaces(id) ON DELETE RESTRICT,
 owner_id uuid NOT NULL REFERENCES public.tsw_owners(id) ON DELETE RESTRICT,
 authorization_digest text NOT NULL CHECK(length(authorization_digest)=64),
 idempotency_key uuid NOT NULL UNIQUE,
 started_session uuid NOT NULL REFERENCES public.tsw_owner_sessions(id) ON DELETE RESTRICT,
 created_at timestamptz NOT NULL DEFAULT now(), stopped_at timestamptz
);
CREATE INDEX tsw_rotation_removals_owner_history_idx ON public.tsw_rotation_removals(owner_id,created_at DESC,preview_id DESC);
CREATE TABLE public.tsw_rotation_removal_slots (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 preview_id uuid NOT NULL REFERENCES public.tsw_rotation_removals(preview_id) ON DELETE RESTRICT,
 workspace_id uuid NOT NULL REFERENCES public.tsw_workspaces(id) ON DELETE RESTRICT,
 platform_member_id text NOT NULL CHECK(length(platform_member_id) BETWEEN 1 AND 255),
 original_account_id uuid NOT NULL REFERENCES public.tsw_target_accounts(id) ON DELETE RESTRICT,
 candidate_account_id uuid NOT NULL REFERENCES public.tsw_target_accounts(id) ON DELETE RESTRICT,
 identifier text NOT NULL, seat_type text NOT NULL CHECK(seat_type='prolite'),
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','lease_acquired','remove_requested','remote_result_uncertain','absent_verification_pending','absent_verified','blocked','stopped')),
 lease_owner uuid, lease_token uuid, lease_epoch bigint NOT NULL DEFAULT 0 CHECK(lease_epoch>=0), lease_expires_at timestamptz,
 attempt_count integer NOT NULL DEFAULT 0 CHECK(attempt_count>=0),
 remote_request_id uuid UNIQUE, requested_at timestamptz, remote_http_status integer,
 verification_id uuid, absent_verified_at timestamptz,
 uncertain_obligation boolean NOT NULL DEFAULT false,
 last_error_code text NOT NULL DEFAULT '', last_error text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(preview_id,platform_member_id), UNIQUE(preview_id,candidate_account_id),
 CHECK((remote_request_id IS NULL)=(requested_at IS NULL)),
 CHECK(NOT uncertain_obligation OR remote_request_id IS NOT NULL),
 CHECK(state<>'absent_verified' OR (verification_id IS NOT NULL AND absent_verified_at IS NOT NULL AND NOT uncertain_obligation)),
 CHECK((lease_owner IS NULL AND lease_token IS NULL AND lease_expires_at IS NULL) OR (lease_owner IS NOT NULL AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL))
);
CREATE INDEX tsw_rotation_removal_obligations ON public.tsw_rotation_removal_slots(workspace_id,platform_member_id);
CREATE TABLE public.tsw_rotation_removal_evidence (
 id uuid PRIMARY KEY,
 slot_id uuid NOT NULL REFERENCES public.tsw_rotation_removal_slots(id) ON DELETE RESTRICT,
 lease_epoch bigint NOT NULL, authorization_digest text NOT NULL CHECK(length(authorization_digest)=64),
 token_exchange_id uuid NOT NULL, owner_evidence_id text NOT NULL CHECK(length(owner_evidence_id)=64),
 observed_at timestamptz NOT NULL, members jsonb NOT NULL CHECK(jsonb_typeof(members)='array'),
 target_absent boolean NOT NULL, complete boolean NOT NULL CHECK(complete),
 created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE public.tsw_rotation_removal_slots ADD CONSTRAINT tsw_rotation_removal_verification_fk FOREIGN KEY(verification_id) REFERENCES public.tsw_rotation_removal_evidence(id) ON DELETE RESTRICT;
REVOKE ALL ON public.tsw_rotation_removals,public.tsw_rotation_removal_slots,public.tsw_rotation_removal_evidence FROM PUBLIC;
-- +goose StatementBegin
CREATE FUNCTION public.tsw_rotation_removal_immutable() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public,pg_temp AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'rotation removal obligations cannot be deleted'; END IF;
 IF TG_TABLE_NAME='tsw_rotation_removal_evidence' THEN RAISE EXCEPTION 'rotation removal evidence is immutable'; END IF;
 IF TG_TABLE_NAME='tsw_rotation_removals' THEN
  IF NEW.preview_id<>OLD.preview_id OR NEW.workspace_id<>OLD.workspace_id OR NEW.owner_id<>OLD.owner_id OR NEW.authorization_digest<>OLD.authorization_digest OR NEW.idempotency_key<>OLD.idempotency_key OR NEW.started_session<>OLD.started_session OR NEW.created_at<>OLD.created_at OR OLD.stopped_at IS NOT NULL OR NEW.stopped_at IS NULL THEN RAISE EXCEPTION 'rotation removal scope is immutable'; END IF;
 ELSE
  IF (NEW.id,NEW.preview_id,NEW.workspace_id,NEW.platform_member_id,NEW.original_account_id,NEW.candidate_account_id,NEW.identifier,NEW.seat_type,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.preview_id,OLD.workspace_id,OLD.platform_member_id,OLD.original_account_id,OLD.candidate_account_id,OLD.identifier,OLD.seat_type,OLD.created_at) OR NEW.lease_epoch<OLD.lease_epoch OR NEW.attempt_count<OLD.attempt_count THEN RAISE EXCEPTION 'rotation slot scope or fencing history changed'; END IF;
  IF OLD.remote_request_id IS NOT NULL AND (NEW.remote_request_id,NEW.requested_at) IS DISTINCT FROM (OLD.remote_request_id,OLD.requested_at) THEN RAISE EXCEPTION 'rotation request obligation is immutable'; END IF;
  IF OLD.state='stopped' AND NEW.state<>'stopped' OR OLD.state='absent_verified' AND NEW.state NOT IN ('absent_verified','stopped','absent_verification_pending') THEN RAISE EXCEPTION 'terminal rotation slot cannot restart'; END IF;
  IF OLD.state='absent_verified' AND NEW.state='absent_verification_pending' AND (NEW.lease_epoch<>OLD.lease_epoch+1 OR NEW.lease_owner IS NULL OR NEW.lease_token IS NULL OR NEW.lease_expires_at<=clock_timestamp() OR NOT NEW.uncertain_obligation OR NEW.attempt_count<>OLD.attempt_count+1) THEN RAISE EXCEPTION 'absence renewal requires a fresh fenced read lease'; END IF;
  IF NEW.state='absent_verified' AND NOT EXISTS(SELECT 1 FROM public.tsw_rotation_removal_evidence e JOIN public.tsw_rotation_removals r ON r.preview_id=NEW.preview_id JOIN public.tsw_expiry_rotation_previews p ON p.id=r.preview_id WHERE e.id=NEW.verification_id AND e.slot_id=NEW.id AND e.lease_epoch=NEW.lease_epoch AND e.target_absent AND e.complete AND e.authorization_digest=r.authorization_digest AND p.status='authorized' AND p.revoked_at IS NULL AND p.expires_at>clock_timestamp() AND r.stopped_at IS NULL) THEN RAISE EXCEPTION 'committed absence evidence and current authorization required'; END IF;
  NEW.updated_at=clock_timestamp();
 END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER tsw_rotation_removal_immutable BEFORE UPDATE OR DELETE ON public.tsw_rotation_removals FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_removal_immutable();
CREATE TRIGGER tsw_rotation_removal_immutable BEFORE UPDATE OR DELETE ON public.tsw_rotation_removal_slots FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_removal_immutable();
CREATE TRIGGER tsw_rotation_removal_immutable BEFORE UPDATE OR DELETE ON public.tsw_rotation_removal_evidence FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_removal_immutable();
-- Shared session action locks span bounded network I/O without holding an epoch
-- transaction. Every covered fact writer takes the corresponding exclusive
-- transaction action lock before advancing its epoch/committing its fact.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.tsw_rotation_epoch_advance(scope_kind text,scope_id uuid) RETURNS void LANGUAGE plpgsql SET search_path=pg_catalog,public,pg_temp AS $$
BEGIN
 IF pg_trigger_depth()=0 THEN RAISE EXCEPTION 'rotation epoch advance requires trigger context'; END IF;
 IF scope_id IS NULL THEN RETURN; END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended('tsw.rotation.action.'||scope_kind||'/'||scope_id::text,0));
 UPDATE public.tsw_rotation_epochs SET version=version+1 WHERE kind=scope_kind AND id=scope_id;
 IF NOT FOUND THEN RAISE EXCEPTION 'missing rotation epoch: % %',scope_kind,scope_id; END IF;
END;
$$;
-- +goose StatementEnd
-- Owner/session revocation is not a source payload, but must not race dispatch.
-- +goose StatementBegin
CREATE FUNCTION public.tsw_rotation_session_gate() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public,pg_temp AS $$
DECLARE oid uuid;
BEGIN
 IF TG_TABLE_NAME='tsw_owners' THEN oid=OLD.id; ELSE oid=OLD.owner_id; END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended('tsw.rotation.action.owner/'||oid::text,0));
 RETURN OLD;
END;
$$;
-- +goose StatementEnd
-- AFTER hooks return value is ignored; guards include auth_version changes.
CREATE TRIGGER tsw_rotation_session_gate AFTER UPDATE OR DELETE ON public.tsw_owner_sessions FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_session_gate();
CREATE TRIGGER tsw_rotation_owner_gate AFTER UPDATE ON public.tsw_owners FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_session_gate();
-- Ticket 12 must consume this gate and recheck authorization/epochs when claiming.
CREATE VIEW public.tsw_rotation_released_slots AS
 SELECT s.* FROM public.tsw_rotation_removal_slots s
 JOIN public.tsw_rotation_removals r ON r.preview_id=s.preview_id
 JOIN public.tsw_expiry_rotation_previews p ON p.id=r.preview_id
 JOIN public.tsw_rotation_removal_evidence e ON e.id=s.verification_id
 WHERE s.state='absent_verified' AND NOT s.uncertain_obligation AND r.stopped_at IS NULL
 AND EXISTS(SELECT 1 FROM public.tsw_owner_sessions original_session JOIN public.tsw_owners original_owner ON original_owner.id=original_session.owner_id WHERE original_session.id=p.authorized_session::uuid AND original_session.owner_id=r.owner_id AND original_session.auth_version=original_owner.auth_version AND original_session.revoked_at IS NULL AND original_session.idle_expires_at>clock_timestamp() AND original_session.absolute_expires_at>clock_timestamp())
 AND p.status='authorized' AND p.revoked_at IS NULL AND p.expires_at>clock_timestamp()
 AND p.authorization_digest=r.authorization_digest AND e.target_absent AND e.complete
 AND e.observed_at>clock_timestamp()-interval '30 seconds'
 AND NOT EXISTS(SELECT 1 FROM jsonb_each_text(p.epoch_versions) v LEFT JOIN public.tsw_rotation_epochs epoch ON v.key=epoch.kind||'/'||epoch.id::text WHERE epoch.version IS NULL OR epoch.version::text<>v.value);
REVOKE ALL ON public.tsw_rotation_released_slots FROM PUBLIC;

-- +goose Down
DROP VIEW public.tsw_rotation_released_slots;
DROP TRIGGER tsw_rotation_owner_gate ON public.tsw_owners;
DROP TRIGGER tsw_rotation_session_gate ON public.tsw_owner_sessions;
DROP FUNCTION public.tsw_rotation_session_gate();
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.tsw_rotation_epoch_advance(scope_kind text,scope_id uuid) RETURNS void LANGUAGE plpgsql SET search_path=pg_catalog,public,pg_temp AS $$
BEGIN
 IF pg_trigger_depth()=0 THEN RAISE EXCEPTION 'rotation epoch advance requires trigger context'; END IF;
 IF scope_id IS NULL THEN RETURN; END IF;
 UPDATE public.tsw_rotation_epochs SET version=version+1 WHERE kind=scope_kind AND id=scope_id;
 IF NOT FOUND THEN RAISE EXCEPTION 'missing rotation epoch: % %',scope_kind,scope_id; END IF;
END;
$$;
-- +goose StatementEnd
ALTER TABLE public.tsw_rotation_removal_slots DROP CONSTRAINT tsw_rotation_removal_verification_fk;
DROP TABLE public.tsw_rotation_removal_evidence;
DROP TABLE public.tsw_rotation_removal_slots;
DROP TABLE public.tsw_rotation_removals;
DROP FUNCTION public.tsw_rotation_removal_immutable();
