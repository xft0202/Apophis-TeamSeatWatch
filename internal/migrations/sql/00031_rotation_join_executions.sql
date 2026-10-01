-- +goose Up
-- Derived execution obligations only; no source epochs or membership facts.
CREATE TABLE public.tsw_rotation_join_executions (
 slot_id uuid PRIMARY KEY REFERENCES public.tsw_rotation_candidate_join_intents(slot_id) ON DELETE RESTRICT,
 preview_id uuid NOT NULL,
 workspace_id uuid NOT NULL,
 owner_id uuid NOT NULL,
 candidate_account_id uuid NOT NULL,
 original_platform_member_id text NOT NULL,
 candidate_identifier text NOT NULL,
 seat_type text NOT NULL,
 authorization_digest text NOT NULL,
 authorized_session uuid NOT NULL,
 epoch_versions jsonb NOT NULL,
 created_at timestamptz NOT NULL,
 state text NOT NULL DEFAULT 'ready' CHECK(state IN ('ready','request_started','request_succeeded','accept_started','reconcile_required','blocked')),
 lease_owner uuid,
 lease_token uuid,
 lease_epoch bigint NOT NULL DEFAULT 0 CHECK(lease_epoch>=0),
 lease_expires_at timestamptz,
 request_attempt_count integer NOT NULL DEFAULT 0 CHECK(request_attempt_count>=0),
 accept_attempt_count integer NOT NULL DEFAULT 0 CHECK(accept_attempt_count>=0),
 request_may_have_reached boolean NOT NULL DEFAULT false,
 accept_may_have_reached boolean NOT NULL DEFAULT false,
 last_outcome text NOT NULL DEFAULT '' CHECK(last_outcome IN ('','started','succeeded','failed','uncertain')),
 last_stage text NOT NULL DEFAULT '' CHECK(last_stage IN ('','request_join','accept_join','reconcile')),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK((lease_owner IS NULL AND lease_token IS NULL AND lease_expires_at IS NULL) OR
       (lease_owner IS NOT NULL AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL AND lease_epoch>0)),
 CHECK(NOT accept_may_have_reached OR request_may_have_reached),
 CHECK(request_may_have_reached=(request_attempt_count>0)),
 CHECK(accept_may_have_reached=(accept_attempt_count>0)),
 CHECK(accept_attempt_count<=request_attempt_count)
);
REVOKE ALL ON public.tsw_rotation_join_executions FROM PUBLIC;
-- +goose StatementBegin
CREATE FUNCTION public.tsw_rotation_join_execution_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public,pg_temp AS $$
DECLARE
 expected_stage text;
 terminal_outcome text;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'rotation join execution obligation cannot be deleted'; END IF;
 IF TG_OP='INSERT' THEN
  IF NOT EXISTS(SELECT 1 FROM public.tsw_rotation_candidate_join_intents i WHERE
   ROW(i.slot_id,i.preview_id,i.workspace_id,i.owner_id,i.candidate_account_id,i.original_platform_member_id,i.candidate_identifier,i.seat_type,i.authorization_digest,i.authorized_session,i.epoch_versions,i.created_at)
   IS NOT DISTINCT FROM ROW(NEW.slot_id,NEW.preview_id,NEW.workspace_id,NEW.owner_id,NEW.candidate_account_id,NEW.original_platform_member_id,NEW.candidate_identifier,NEW.seat_type,NEW.authorization_digest,NEW.authorized_session,NEW.epoch_versions,NEW.created_at))
  THEN RAISE EXCEPTION 'rotation join execution must match original intent'; END IF;
  IF NEW.state<>'ready' OR NEW.lease_epoch<>0 OR NEW.lease_owner IS NOT NULL OR NEW.request_attempt_count<>0 OR NEW.accept_attempt_count<>0 OR NEW.request_may_have_reached OR NEW.accept_may_have_reached OR NEW.last_stage<>'' OR NEW.last_outcome<>''
  THEN RAISE EXCEPTION 'rotation join execution must start unclaimed'; END IF;
 ELSE
  IF ROW(NEW.slot_id,NEW.preview_id,NEW.workspace_id,NEW.owner_id,NEW.candidate_account_id,NEW.original_platform_member_id,NEW.candidate_identifier,NEW.seat_type,NEW.authorization_digest,NEW.authorized_session,NEW.epoch_versions,NEW.created_at)
   IS DISTINCT FROM ROW(OLD.slot_id,OLD.preview_id,OLD.workspace_id,OLD.owner_id,OLD.candidate_account_id,OLD.original_platform_member_id,OLD.candidate_identifier,OLD.seat_type,OLD.authorization_digest,OLD.authorized_session,OLD.epoch_versions,OLD.created_at)
  THEN RAISE EXCEPTION 'rotation join execution binding is immutable'; END IF;
  IF NEW.lease_epoch<OLD.lease_epoch OR NEW.request_attempt_count<OLD.request_attempt_count OR NEW.accept_attempt_count<OLD.accept_attempt_count OR (OLD.request_may_have_reached AND NOT NEW.request_may_have_reached) OR (OLD.accept_may_have_reached AND NOT NEW.accept_may_have_reached)
  THEN RAISE EXCEPTION 'rotation join execution markers cannot regress'; END IF;
  IF ROW(NEW.state,NEW.lease_owner,NEW.lease_token,NEW.lease_epoch,NEW.lease_expires_at,NEW.request_attempt_count,NEW.accept_attempt_count,NEW.request_may_have_reached,NEW.accept_may_have_reached,NEW.last_stage,NEW.last_outcome)
   IS NOT DISTINCT FROM ROW(OLD.state,OLD.lease_owner,OLD.lease_token,OLD.lease_epoch,OLD.lease_expires_at,OLD.request_attempt_count,OLD.accept_attempt_count,OLD.request_may_have_reached,OLD.accept_may_have_reached,OLD.last_stage,OLD.last_outcome)
  THEN RETURN NEW; END IF;
  IF NEW.lease_epoch<>OLD.lease_epoch THEN
   IF NEW.lease_epoch<>OLD.lease_epoch+1 OR OLD.lease_expires_at>clock_timestamp() OR NEW.lease_owner IS NULL OR NEW.lease_token IS NOT DISTINCT FROM OLD.lease_token OR NEW.lease_expires_at IS NULL OR NEW.lease_expires_at<=clock_timestamp()
    OR ROW(NEW.request_attempt_count,NEW.accept_attempt_count,NEW.request_may_have_reached,NEW.accept_may_have_reached) IS DISTINCT FROM ROW(OLD.request_attempt_count,OLD.accept_attempt_count,OLD.request_may_have_reached,OLD.accept_may_have_reached)
    OR (NOT OLD.request_may_have_reached AND (OLD.state<>'ready' OR NEW.state<>'ready' OR ROW(NEW.last_stage,NEW.last_outcome) IS DISTINCT FROM ROW(OLD.last_stage,OLD.last_outcome)))
    OR (OLD.request_may_have_reached AND ROW(NEW.state,NEW.last_stage,NEW.last_outcome) IS DISTINCT FROM ROW('reconcile_required','reconcile','started'))
   THEN RAISE EXCEPTION 'rotation join execution requires legal lease claim'; END IF;
  ELSE
   IF OLD.lease_owner IS NULL OR OLD.lease_expires_at<=clock_timestamp() THEN RAISE EXCEPTION 'rotation join execution requires live lease'; END IF;
   IF (OLD.state='ready' AND NEW.state='request_started') OR (OLD.state='request_succeeded' AND NEW.state='accept_started') THEN
    expected_stage:=CASE WHEN NEW.state='request_started' THEN 'request_join' ELSE 'accept_join' END;
    IF ROW(NEW.lease_owner,NEW.lease_token,NEW.lease_expires_at) IS DISTINCT FROM ROW(OLD.lease_owner,OLD.lease_token,OLD.lease_expires_at)
     OR NEW.request_attempt_count<>OLD.request_attempt_count+(CASE WHEN expected_stage='request_join' THEN 1 ELSE 0 END)
     OR NEW.accept_attempt_count<>OLD.accept_attempt_count+(CASE WHEN expected_stage='accept_join' THEN 1 ELSE 0 END)
     OR NOT NEW.request_may_have_reached OR NEW.accept_may_have_reached<>(expected_stage='accept_join')
     OR ROW(NEW.last_stage,NEW.last_outcome) IS DISTINCT FROM ROW(expected_stage,'started')
    THEN RAISE EXCEPTION 'rotation join execution requires exact stage start markers'; END IF;
    IF expected_stage='accept_join' AND NOT EXISTS(SELECT 1 FROM public.tsw_rotation_join_execution_attempts a WHERE a.slot_id=OLD.slot_id AND a.lease_epoch=OLD.lease_epoch AND a.stage='request_join' AND a.attempt_no=OLD.request_attempt_count AND a.event_kind='finished' AND a.outcome='succeeded')
    THEN RAISE EXCEPTION 'rotation join accept requires successful request event'; END IF;
   ELSE
    expected_stage:=CASE OLD.state WHEN 'request_started' THEN 'request_join' WHEN 'accept_started' THEN 'accept_join' WHEN 'reconcile_required' THEN 'reconcile' ELSE NULL END;
    SELECT a.outcome INTO terminal_outcome FROM public.tsw_rotation_join_execution_attempts a
     WHERE a.slot_id=OLD.slot_id AND a.lease_epoch=OLD.lease_epoch AND a.stage=expected_stage AND a.event_kind='finished'
      AND (expected_stage='reconcile' OR a.attempt_no=CASE WHEN expected_stage='request_join' THEN OLD.request_attempt_count ELSE OLD.accept_attempt_count END);
    IF terminal_outcome IS NULL OR ROW(NEW.request_attempt_count,NEW.accept_attempt_count,NEW.request_may_have_reached,NEW.accept_may_have_reached) IS DISTINCT FROM ROW(OLD.request_attempt_count,OLD.accept_attempt_count,OLD.request_may_have_reached,OLD.accept_may_have_reached)
     OR ROW(NEW.last_stage,NEW.last_outcome) IS DISTINCT FROM ROW(expected_stage,terminal_outcome)
    THEN RAISE EXCEPTION 'rotation join execution requires matching terminal event'; END IF;
    IF expected_stage='request_join' AND terminal_outcome='succeeded' THEN
     IF NEW.state<>'request_succeeded' OR ROW(NEW.lease_owner,NEW.lease_token,NEW.lease_expires_at) IS DISTINCT FROM ROW(OLD.lease_owner,OLD.lease_token,OLD.lease_expires_at)
     THEN RAISE EXCEPTION 'rotation join request success must retain lease'; END IF;
    ELSIF NEW.state<>'reconcile_required' OR NEW.lease_owner IS NOT NULL OR NEW.lease_token IS NOT NULL OR NEW.lease_expires_at IS NOT NULL THEN
     RAISE EXCEPTION 'rotation join terminal outcome requires released reconciliation';
    END IF;
   END IF;
  END IF;
 END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER tsw_rotation_join_execution_guard BEFORE INSERT OR UPDATE OR DELETE ON public.tsw_rotation_join_executions FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_join_execution_guard();

CREATE TABLE public.tsw_rotation_join_execution_attempts (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 slot_id uuid NOT NULL REFERENCES public.tsw_rotation_join_executions(slot_id) ON DELETE RESTRICT,
 lease_epoch bigint NOT NULL CHECK(lease_epoch>0),
 stage text NOT NULL CHECK(stage IN ('request_join','accept_join','reconcile')),
 attempt_no integer NOT NULL CHECK(attempt_no>0),
 event_kind text NOT NULL CHECK(event_kind IN ('started','finished')),
 start_event_id uuid REFERENCES public.tsw_rotation_join_execution_attempts(id) ON DELETE RESTRICT,
 may_have_reached boolean NOT NULL,
 outcome text NOT NULL CHECK(outcome IN ('started','succeeded','failed','uncertain')),
 started_at timestamptz NOT NULL,
 finished_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(slot_id,stage,attempt_no,event_kind),
 CHECK((event_kind='started' AND outcome='started' AND finished_at IS NULL AND start_event_id IS NULL) OR
       (event_kind='finished' AND outcome<>'started' AND finished_at>=started_at AND finished_at IS NOT NULL AND start_event_id IS NOT NULL)),
 CHECK(stage<>'reconcile' OR NOT may_have_reached)
);
REVOKE ALL ON public.tsw_rotation_join_execution_attempts FROM PUBLIC;
-- +goose StatementBegin
CREATE FUNCTION public.tsw_rotation_join_attempt_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public,pg_temp AS $$
BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'rotation join attempt journal is append-only'; END IF;
 IF NOT EXISTS(SELECT 1 FROM public.tsw_rotation_join_executions e WHERE e.slot_id=NEW.slot_id AND e.lease_epoch=NEW.lease_epoch AND e.lease_expires_at>clock_timestamp()
  AND e.last_stage=NEW.stage AND e.last_outcome='started'
 AND ((NEW.stage='request_join' AND e.state='request_started' AND NEW.attempt_no=e.request_attempt_count) OR (NEW.stage='accept_join' AND e.state='accept_started' AND NEW.attempt_no=e.accept_attempt_count) OR (NEW.stage='reconcile' AND e.state='reconcile_required'))
 AND (NEW.event_kind='finished' OR NEW.stage='reconcile' OR NEW.may_have_reached))
 THEN RAISE EXCEPTION 'rotation join attempt requires current execution lease and marker'; END IF;
 IF NEW.event_kind='started' AND NEW.stage='reconcile' AND (NEW.attempt_no<>(SELECT COALESCE(max(a.attempt_no),0)+1 FROM public.tsw_rotation_join_execution_attempts a WHERE a.slot_id=NEW.slot_id AND a.stage='reconcile') OR EXISTS(SELECT 1 FROM public.tsw_rotation_join_execution_attempts a WHERE a.slot_id=NEW.slot_id AND a.stage='reconcile' AND a.lease_epoch=NEW.lease_epoch))
 THEN RAISE EXCEPTION 'rotation join reconciliation requires one numbered start per lease'; END IF;
 IF NEW.event_kind='finished' AND NOT EXISTS(SELECT 1 FROM public.tsw_rotation_join_execution_attempts a WHERE a.id=NEW.start_event_id AND a.event_kind='started'
  AND ROW(a.slot_id,a.stage,a.attempt_no,a.lease_epoch,a.started_at) IS NOT DISTINCT FROM ROW(NEW.slot_id,NEW.stage,NEW.attempt_no,NEW.lease_epoch,NEW.started_at))
 THEN RAISE EXCEPTION 'rotation join terminal event must match original start'; END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER tsw_rotation_join_attempt_guard BEFORE INSERT OR UPDATE OR DELETE ON public.tsw_rotation_join_execution_attempts FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_join_attempt_guard();

-- Starts update the execution before appending the event in the same transaction.
-- Check the captured transition at commit, not the latest row (which may already
-- have advanced), so neither autocommit SQL nor SET CONSTRAINTS can leave a half start.
-- +goose StatementBegin
CREATE FUNCTION public.tsw_rotation_join_execution_start_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public,pg_temp AS $$
DECLARE
 expected_stage text;
BEGIN
 IF NEW.lease_epoch<>OLD.lease_epoch AND NEW.state='reconcile_required' THEN expected_stage:='reconcile';
 ELSIF OLD.state='ready' AND NEW.state='request_started' THEN expected_stage:='request_join';
 ELSIF OLD.state='request_succeeded' AND NEW.state='accept_started' THEN expected_stage:='accept_join';
 ELSE RETURN NULL;
 END IF;
 IF NOT EXISTS(SELECT 1 FROM public.tsw_rotation_join_execution_attempts a WHERE a.slot_id=NEW.slot_id AND a.lease_epoch=NEW.lease_epoch AND a.stage=expected_stage AND a.event_kind='started'
  AND (expected_stage='reconcile' OR a.attempt_no=CASE WHEN expected_stage='request_join' THEN NEW.request_attempt_count ELSE NEW.accept_attempt_count END))
 THEN RAISE EXCEPTION 'rotation join execution requires matching start event'; END IF;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER tsw_rotation_join_execution_start_guard AFTER UPDATE ON public.tsw_rotation_join_executions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_join_execution_start_guard();

-- +goose Down
DROP TRIGGER tsw_rotation_join_execution_start_guard ON public.tsw_rotation_join_executions;
DROP FUNCTION public.tsw_rotation_join_execution_start_guard();
DROP TABLE public.tsw_rotation_join_execution_attempts;
DROP FUNCTION public.tsw_rotation_join_attempt_guard();
DROP TABLE public.tsw_rotation_join_executions;
DROP FUNCTION public.tsw_rotation_join_execution_guard();
