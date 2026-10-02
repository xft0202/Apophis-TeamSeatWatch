-- +goose Up
-- Frozen Personal generation actually admitted for the original request.
CREATE TABLE public.tsw_rotation_join_personal_bindings (
 slot_id uuid PRIMARY KEY REFERENCES public.tsw_rotation_join_executions(slot_id) ON DELETE RESTRICT,
 candidate_account_id uuid NOT NULL,
 workspace_id uuid NOT NULL,
 platform_workspace_id text NOT NULL CHECK(length(platform_workspace_id) BETWEEN 1 AND 255),
 candidate_identifier text NOT NULL,
 seat_type text NOT NULL CHECK(seat_type='prolite'),
 secret_revision bigint NOT NULL CHECK(secret_revision>0),
 attempt bigint NOT NULL CHECK(attempt>0),
 generation uuid NOT NULL,
 key_version smallint NOT NULL CHECK(key_version>0),
 sealed_digest text NOT NULL CHECK(sealed_digest ~ '^[a-f0-9]{64}$'),
 db_expiry timestamptz NOT NULL,
 decoded_expiry timestamptz NOT NULL CHECK(decoded_expiry=db_expiry),
 subject_id text NOT NULL CHECK(length(subject_id) BETWEEN 1 AND 255),
 identity_observed_at timestamptz NOT NULL,
 bound_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 source text NOT NULL CHECK(source='personal_identity_confirmation'),
 lease_epoch bigint NOT NULL,
 lease_owner uuid NOT NULL,
 lease_token uuid NOT NULL,
 owner_session uuid NOT NULL,
 request_start_event_id uuid NOT NULL REFERENCES public.tsw_rotation_join_execution_attempts(id) ON DELETE RESTRICT
);
REVOKE ALL ON public.tsw_rotation_join_personal_bindings FROM PUBLIC;
-- +goose StatementBegin
CREATE FUNCTION public.tsw_rotation_join_personal_binding_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public,pg_temp AS $$
DECLARE request_start uuid;
BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'rotation join Personal binding is append-only'; END IF;
 -- xmin is database-owned: a committed historical marker cannot be rebound.
 -- A conservative exact top-level xid match also rejects subtransaction starts.
 SELECT j.id INTO request_start FROM public.tsw_rotation_join_execution_attempts j
 JOIN public.tsw_rotation_join_executions e USING(slot_id)
 WHERE j.slot_id=NEW.slot_id AND j.lease_epoch=NEW.lease_epoch
 AND j.stage='request_join' AND j.event_kind='started' AND j.attempt_no=e.request_attempt_count
 AND j.xmin::text::bigint=mod(pg_current_xact_id()::text::numeric,4294967296)::bigint;
 IF request_start IS NULL OR (NEW.request_start_event_id IS NOT NULL AND NEW.request_start_event_id<>request_start) THEN
  RAISE EXCEPTION 'Personal binding must accompany exact request start in the same transaction';
 END IF;
 NEW.request_start_event_id:=request_start;
 IF NOT EXISTS(SELECT 1 FROM public.tsw_rotation_candidate_join_intents i
 JOIN public.tsw_rotation_join_executions e USING(slot_id)
 JOIN public.tsw_workspaces w ON w.id=i.workspace_id
 JOIN public.tsw_target_accounts target ON target.id=i.candidate_account_id
 JOIN public.tsw_target_credentials c ON c.target_account_id=target.id
 JOIN public.tsw_target_personal_access a ON a.target_account_id=c.target_account_id AND a.secret_revision=c.secret_revision AND a.status='ready'
 JOIN public.tsw_target_personal_sessions s ON s.target_account_id=a.target_account_id AND s.secret_revision=a.secret_revision AND s.attempt=a.attempt
 JOIN public.tsw_owner_sessions os ON os.id=NEW.owner_session AND os.owner_id=i.owner_id
 JOIN public.tsw_owners o ON o.id=os.owner_id
 WHERE i.slot_id=NEW.slot_id AND target.status='active' AND target.identifier=i.candidate_identifier
 AND ROW(i.candidate_account_id,i.workspace_id,w.platform_workspace_id,i.candidate_identifier,i.seat_type)
 IS NOT DISTINCT FROM ROW(NEW.candidate_account_id,NEW.workspace_id,NEW.platform_workspace_id,NEW.candidate_identifier,NEW.seat_type)
 AND ROW(s.secret_revision,s.attempt,s.generation,s.key_version,encode(sha256(s.sealed_session),'hex'),s.expires_at)
 IS NOT DISTINCT FROM ROW(NEW.secret_revision,NEW.attempt,NEW.generation,NEW.key_version,NEW.sealed_digest,NEW.db_expiry)
 AND s.expires_at>clock_timestamp()+interval '1 minute'
 AND NEW.identity_observed_at<=clock_timestamp() AND NEW.identity_observed_at>clock_timestamp()-interval '30 seconds'
 AND (c.platform_subject_id IS NULL OR c.platform_subject_id=NEW.subject_id)
 AND os.auth_version=o.auth_version AND os.revoked_at IS NULL AND os.idle_expires_at>clock_timestamp() AND os.absolute_expires_at>clock_timestamp()
 AND e.state='request_started' AND e.lease_epoch=NEW.lease_epoch AND e.lease_owner=NEW.lease_owner AND e.lease_token=NEW.lease_token AND e.lease_expires_at>clock_timestamp()
 AND EXISTS(SELECT 1 FROM public.tsw_rotation_join_execution_attempts j WHERE j.slot_id=e.slot_id AND j.lease_epoch=e.lease_epoch AND j.stage='request_join' AND j.event_kind='started'))
 THEN RAISE EXCEPTION 'rotation join Personal binding requires exact admitted request'; END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER tsw_rotation_join_personal_binding_guard BEFORE INSERT OR UPDATE OR DELETE ON public.tsw_rotation_join_personal_bindings FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_join_personal_binding_guard();

-- New request markers require the binding at commit; historical journal rows
-- are deliberately not scanned, amended, or retroactively authorized.
-- +goose StatementBegin
CREATE FUNCTION public.tsw_rotation_join_request_binding_pair_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public,pg_temp AS $$
BEGIN
 IF NEW.stage='request_join' AND NEW.event_kind='started' AND NOT EXISTS(
  SELECT 1 FROM public.tsw_rotation_join_personal_bindings b
  WHERE b.slot_id=NEW.slot_id AND b.lease_epoch=NEW.lease_epoch AND b.request_start_event_id=NEW.id
 ) THEN RAISE EXCEPTION 'request start requires atomic Personal binding'; END IF;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER tsw_rotation_join_request_binding_pair_guard AFTER INSERT ON public.tsw_rotation_join_execution_attempts DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_join_request_binding_pair_guard();

-- Read-only post-accept evidence. This is never membership completion,
-- credentials, usage, delivery, or a released-slot update.
-- +goose StatementBegin
CREATE FUNCTION public.tsw_rotation_join_membership_digest(slot uuid,attempt integer,observed timestamptz,source text,result text,member_id text,identifier text,seat text,workspace uuid,epoch bigint,diagnostic text) RETURNS text LANGUAGE sql IMMUTABLE SET search_path=pg_catalog,public,pg_temp AS $$
 SELECT encode(sha256(convert_to(jsonb_build_array(slot,attempt,to_char(observed AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),source,result,COALESCE(member_id,''),identifier,seat,workspace,epoch,diagnostic)::text,'UTF8')),'hex');
$$;
-- +goose StatementEnd
CREATE TABLE public.tsw_rotation_join_membership_evidence (
 slot_id uuid NOT NULL REFERENCES public.tsw_rotation_join_executions(slot_id) ON DELETE RESTRICT,
 reconciliation_attempt integer NOT NULL CHECK(reconciliation_attempt > 0),
 observed_at timestamptz NOT NULL,
 source text NOT NULL CHECK(source='personal_session_members'),
 result text NOT NULL CHECK(result IN ('confirmed','absent','unknown','invalid')),
 platform_member_id text,
 candidate_identifier text NOT NULL,
 seat_type text NOT NULL,
 workspace_id uuid NOT NULL,
 lease_epoch bigint NOT NULL CHECK(lease_epoch > 0),
 lease_owner uuid NOT NULL,
 lease_token uuid NOT NULL,
 owner_session uuid NOT NULL,
 diagnostic text NOT NULL CHECK(diagnostic IN ('membership_unknown','membership_unauthorized','membership_incomplete','membership_duplicate_identifier','membership_duplicate_identity','reconcile_absent','member_not_confirmed','member_seat_invalid','member_confirmed','admission_changed','dispatch_personal_binding_changed','candidate_identity_changed','candidate_generation_changed','membership_stale')),
 evidence_digest text NOT NULL CHECK(evidence_digest ~ '^[a-f0-9]{64}$'),
 reconcile_start_event_id uuid NOT NULL REFERENCES public.tsw_rotation_join_execution_attempts(id) ON DELETE RESTRICT,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(slot_id, reconciliation_attempt),
 UNIQUE(slot_id, evidence_digest),
 CHECK((result='confirmed' AND platform_member_id IS NOT NULL AND length(platform_member_id) BETWEEN 1 AND 255) OR
       (result<>'confirmed' AND platform_member_id IS NULL))
);
REVOKE ALL ON public.tsw_rotation_join_membership_evidence FROM PUBLIC;
-- +goose StatementBegin
CREATE FUNCTION public.tsw_rotation_join_membership_evidence_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public,pg_temp AS $$
DECLARE reconcile_start uuid;
BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'rotation join membership evidence is append-only'; END IF;
 SELECT a.id INTO reconcile_start FROM public.tsw_rotation_join_execution_attempts a
 WHERE a.slot_id=NEW.slot_id AND a.lease_epoch=NEW.lease_epoch AND a.stage='reconcile'
 AND a.attempt_no=NEW.reconciliation_attempt AND a.event_kind='started' AND NOT a.may_have_reached;
 IF reconcile_start IS NULL OR (NEW.reconcile_start_event_id IS NOT NULL AND NEW.reconcile_start_event_id<>reconcile_start) THEN
  RAISE EXCEPTION 'membership evidence must bind exact reconciliation start';
 END IF;
 NEW.reconcile_start_event_id:=reconcile_start;
 IF NOT EXISTS(
  SELECT 1 FROM public.tsw_rotation_candidate_join_intents i
  JOIN public.tsw_rotation_join_executions e ON e.slot_id=i.slot_id
  WHERE i.slot_id=NEW.slot_id
    AND ROW(NEW.candidate_identifier,NEW.seat_type,NEW.workspace_id)
      IS NOT DISTINCT FROM ROW(i.candidate_identifier,i.seat_type,i.workspace_id)
    AND e.state='reconcile_required'
    AND e.lease_epoch=NEW.lease_epoch
    AND e.lease_owner=NEW.lease_owner AND e.lease_token=NEW.lease_token
    AND EXISTS(SELECT 1 FROM public.tsw_owner_sessions s JOIN public.tsw_owners o ON o.id=s.owner_id WHERE s.id=NEW.owner_session AND s.owner_id=i.owner_id AND s.auth_version=o.auth_version AND s.revoked_at IS NULL AND s.idle_expires_at>clock_timestamp() AND s.absolute_expires_at>clock_timestamp())
    AND e.lease_expires_at>clock_timestamp()
    AND EXISTS(SELECT 1 FROM public.tsw_rotation_join_execution_attempts a
      WHERE a.slot_id=e.slot_id AND a.lease_epoch=e.lease_epoch
        AND a.stage='reconcile' AND a.attempt_no=NEW.reconciliation_attempt
        AND a.event_kind='started' AND a.may_have_reached=false)
 ) THEN RAISE EXCEPTION 'rotation join membership evidence binding is invalid'; END IF;
 IF NEW.evidence_digest<>public.tsw_rotation_join_membership_digest(NEW.slot_id,NEW.reconciliation_attempt,NEW.observed_at,NEW.source,NEW.result,NEW.platform_member_id,NEW.candidate_identifier,NEW.seat_type,NEW.workspace_id,NEW.lease_epoch,NEW.diagnostic) THEN RAISE EXCEPTION 'membership evidence digest is invalid'; END IF;
 IF NEW.observed_at>clock_timestamp() OR NEW.observed_at<clock_timestamp()-interval '30 seconds' THEN RAISE EXCEPTION 'membership observation is stale'; END IF;
 IF NEW.result='confirmed' AND (NEW.seat_type<>'prolite' OR NEW.platform_member_id IS NULL OR NOT EXISTS(SELECT 1 FROM public.tsw_rotation_join_personal_bindings b WHERE b.slot_id=NEW.slot_id)) THEN
  RAISE EXCEPTION 'confirmed membership evidence is invalid';
 END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER tsw_rotation_join_membership_evidence_guard
 BEFORE INSERT OR UPDATE OR DELETE ON public.tsw_rotation_join_membership_evidence
 FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_join_membership_evidence_guard();

-- Evidence precedes its terminal receipt within the runtime transaction. Only
-- the exact compatible pair may become durable, including SET CONSTRAINTS.
-- +goose StatementBegin
CREATE FUNCTION public.tsw_rotation_join_membership_terminal_pair_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public,pg_temp AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM public.tsw_rotation_join_execution_attempts a
 JOIN public.tsw_rotation_join_execution_attempts f ON f.start_event_id=a.id
 WHERE a.id=NEW.reconcile_start_event_id AND a.slot_id=NEW.slot_id
 AND a.stage='reconcile' AND a.event_kind='started' AND a.lease_epoch=NEW.lease_epoch
 AND a.attempt_no=NEW.reconciliation_attempt AND NOT a.may_have_reached
 AND ROW(f.slot_id,f.stage,f.lease_epoch,f.attempt_no,f.started_at)
 IS NOT DISTINCT FROM ROW(a.slot_id,a.stage,a.lease_epoch,a.attempt_no,a.started_at)
 AND f.event_kind='finished' AND NOT f.may_have_reached
 AND f.outcome=CASE WHEN NEW.result IN ('confirmed','absent') THEN 'succeeded' ELSE 'uncertain' END)
 THEN RAISE EXCEPTION 'membership evidence requires exact compatible terminal receipt'; END IF;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER tsw_rotation_join_membership_terminal_pair_guard AFTER INSERT ON public.tsw_rotation_join_membership_evidence DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_join_membership_terminal_pair_guard();

-- +goose Down
DROP TRIGGER tsw_rotation_join_membership_terminal_pair_guard ON public.tsw_rotation_join_membership_evidence;
DROP FUNCTION public.tsw_rotation_join_membership_terminal_pair_guard();
DROP TRIGGER tsw_rotation_join_request_binding_pair_guard ON public.tsw_rotation_join_execution_attempts;
DROP FUNCTION public.tsw_rotation_join_request_binding_pair_guard();
DROP TABLE public.tsw_rotation_join_personal_bindings;
DROP FUNCTION public.tsw_rotation_join_personal_binding_guard();
DROP TRIGGER tsw_rotation_join_membership_evidence_guard ON public.tsw_rotation_join_membership_evidence;
DROP FUNCTION public.tsw_rotation_join_membership_evidence_guard();
DROP TABLE public.tsw_rotation_join_membership_evidence;
DROP FUNCTION public.tsw_rotation_join_membership_digest(uuid,integer,timestamptz,text,text,text,text,text,uuid,bigint,text);
