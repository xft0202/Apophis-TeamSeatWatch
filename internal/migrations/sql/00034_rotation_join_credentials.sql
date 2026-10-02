-- +goose Up
-- Dedicated original-intent child credentials; no delivery or usage tables.
CREATE TABLE public.tsw_rotation_join_credential_attempts (
 slot_id uuid PRIMARY KEY REFERENCES public.tsw_rotation_join_personal_bindings(slot_id) ON DELETE RESTRICT,
 attempt_id uuid NOT NULL UNIQUE,
 generation uuid NOT NULL UNIQUE,
 candidate_account_id uuid NOT NULL,
 workspace_id uuid NOT NULL,
 platform_workspace_id text NOT NULL,
 secret_revision bigint NOT NULL CHECK(secret_revision>0),
 subject_id text NOT NULL CHECK(length(subject_id) BETWEEN 1 AND 255),
 membership_attempt integer NOT NULL,
 membership_digest text NOT NULL CHECK(membership_digest ~ '^[a-f0-9]{64}$'),
 platform_member_id text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 lease_owner uuid,
 lease_token uuid,
 lease_epoch bigint NOT NULL DEFAULT 0 CHECK(lease_epoch>=0),
 lease_expires_at timestamptz,
 owner_session uuid,
 FOREIGN KEY(slot_id,membership_attempt) REFERENCES public.tsw_rotation_join_membership_evidence(slot_id,reconciliation_attempt) ON DELETE RESTRICT,
 CHECK((lease_owner IS NULL AND lease_token IS NULL AND lease_expires_at IS NULL AND owner_session IS NULL) OR
       (lease_owner IS NOT NULL AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL AND owner_session IS NOT NULL AND lease_epoch>0))
);
CREATE TABLE public.tsw_rotation_join_credential_events (
 attempt_id uuid NOT NULL REFERENCES public.tsw_rotation_join_credential_attempts(attempt_id) ON DELETE RESTRICT,
 stage text NOT NULL CHECK(stage IN ('web','oauth')),
 event text NOT NULL CHECK(event IN ('started','review_required')),
 lease_epoch bigint NOT NULL,
 lease_owner uuid NOT NULL,
 lease_token uuid NOT NULL,
 owner_session uuid NOT NULL,
 observed_at timestamptz NOT NULL,
 PRIMARY KEY(attempt_id,stage,event)
);
CREATE TABLE public.tsw_rotation_join_credential_components (
 attempt_id uuid NOT NULL REFERENCES public.tsw_rotation_join_credential_attempts(attempt_id) ON DELETE RESTRICT,
 kind text NOT NULL CHECK(kind IN ('web','oauth')),
 key_version smallint NOT NULL CHECK(key_version>0),
 nonce bytea NOT NULL CHECK(octet_length(nonce)=12),
 sealed bytea NOT NULL CHECK(octet_length(sealed) BETWEEN 17 AND 131072),
 sealed_digest text NOT NULL CHECK(sealed_digest ~ '^[a-f0-9]{64}$'),
 observed_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 lease_epoch bigint NOT NULL,
 lease_owner uuid NOT NULL,
 lease_token uuid NOT NULL,
 owner_session uuid NOT NULL,
 PRIMARY KEY(attempt_id,kind),
 CHECK(expires_at>observed_at+interval '1 minute' AND expires_at<=observed_at+interval '24 hours')
);
CREATE TABLE public.tsw_rotation_join_credential_generations (
 attempt_id uuid PRIMARY KEY REFERENCES public.tsw_rotation_join_credential_attempts(attempt_id) ON DELETE RESTRICT,
 generation uuid NOT NULL UNIQUE,
 candidate_account_id uuid NOT NULL,
 workspace_id uuid NOT NULL,
 web_kind text NOT NULL DEFAULT 'web' CHECK(web_kind='web'),
 oauth_kind text NOT NULL DEFAULT 'oauth' CHECK(oauth_kind='oauth'),
 web_digest text NOT NULL,
 oauth_digest text NOT NULL,
 membership_digest text NOT NULL,
 verified_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 lease_epoch bigint NOT NULL,
 lease_owner uuid NOT NULL,
 lease_token uuid NOT NULL,
 owner_session uuid NOT NULL,
 FOREIGN KEY(attempt_id,web_kind) REFERENCES public.tsw_rotation_join_credential_components(attempt_id,kind) ON DELETE RESTRICT,
 FOREIGN KEY(attempt_id,oauth_kind) REFERENCES public.tsw_rotation_join_credential_components(attempt_id,kind) ON DELETE RESTRICT
);
REVOKE ALL ON public.tsw_rotation_join_credential_attempts,public.tsw_rotation_join_credential_events,public.tsw_rotation_join_credential_components,public.tsw_rotation_join_credential_generations FROM PUBLIC;

-- Write-clock authority and the complete pinned Personal tuple remain required
-- even during repair; a historical unbound journal cannot acquire credentials.
-- +goose StatementBegin
CREATE FUNCTION public.tsw_rotation_join_credential_authority(slot uuid,session uuid) RETURNS boolean LANGUAGE sql VOLATILE SET search_path=pg_catalog,public,pg_temp AS $$
 SELECT EXISTS(SELECT 1 FROM public.tsw_rotation_candidate_join_intents i
 JOIN public.tsw_rotation_join_executions e USING(slot_id)
 JOIN public.tsw_rotation_join_personal_bindings b USING(slot_id)
 JOIN public.tsw_rotation_released_slots released ON released.id=i.slot_id AND released.verification_id=i.released_verification_id
 JOIN public.tsw_expiry_rotation_previews p ON p.id=i.preview_id
 JOIN public.tsw_workspaces w ON w.id=i.workspace_id
 JOIN public.tsw_target_accounts target ON target.id=i.candidate_account_id
 JOIN public.tsw_target_credentials c ON c.target_account_id=target.id
 JOIN public.tsw_target_personal_access a ON a.target_account_id=target.id AND a.secret_revision=c.secret_revision AND a.status='ready'
 JOIN public.tsw_target_personal_sessions s ON s.target_account_id=a.target_account_id AND s.secret_revision=a.secret_revision AND s.attempt=a.attempt
 JOIN public.tsw_owner_sessions os ON os.id=session AND os.owner_id=i.owner_id
 JOIN public.tsw_owners o ON o.id=os.owner_id
 WHERE i.slot_id=slot AND e.state='reconcile_required' AND target.status='active' AND target.identifier=i.candidate_identifier
 AND p.authorization_digest=i.authorization_digest AND p.authorized_session::uuid=i.authorized_session AND p.epoch_versions=i.epoch_versions
 AND ROW(b.candidate_account_id,b.workspace_id,b.platform_workspace_id,b.candidate_identifier,b.seat_type)
 IS NOT DISTINCT FROM ROW(i.candidate_account_id,i.workspace_id,w.platform_workspace_id,i.candidate_identifier,i.seat_type)
 AND ROW(b.secret_revision,b.attempt,b.generation,b.key_version,b.sealed_digest,b.db_expiry)
 IS NOT DISTINCT FROM ROW(s.secret_revision,s.attempt,s.generation,s.key_version,encode(sha256(s.sealed_session),'hex'),s.expires_at)
 AND b.decoded_expiry=b.db_expiry AND s.expires_at>clock_timestamp()+interval '1 minute'
 AND c.materials_sealed AND c.material_status='complete' AND (c.platform_subject_id IS NULL OR c.platform_subject_id=b.subject_id)
 AND EXISTS(SELECT 1 FROM public.tsw_rotation_join_membership_evidence latest
  WHERE latest.slot_id=i.slot_id AND latest.reconciliation_attempt=(SELECT max(reconciliation_attempt) FROM public.tsw_rotation_join_membership_evidence WHERE slot_id=i.slot_id)
  AND latest.result='confirmed' AND NOT EXISTS(SELECT 1 FROM public.tsw_rotation_join_credential_attempts ca WHERE ca.slot_id=i.slot_id AND ca.platform_member_id<>latest.platform_member_id))
 AND NOT EXISTS(SELECT 1 FROM public.tsw_rotation_global_protections protection WHERE protection.target_account_id=target.id AND protection.status<>'none')
 AND NOT EXISTS(SELECT 1 FROM public.tsw_rotation_usage_ledger u WHERE u.target_account_id=target.id AND (u.ever_used OR u.usage_state<>'never_used' OR u.expires_at<=clock_timestamp()))
 AND os.auth_version=o.auth_version AND os.revoked_at IS NULL AND os.idle_expires_at>clock_timestamp() AND os.absolute_expires_at>clock_timestamp());
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION public.tsw_rotation_join_credential_attempt_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public,pg_temp AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'credential attempt cannot be erased'; END IF;
 IF TG_OP='INSERT' THEN
  IF NEW.lease_epoch<>0 OR NEW.lease_owner IS NOT NULL OR NOT EXISTS(SELECT 1 FROM public.tsw_rotation_join_personal_bindings b
   JOIN public.tsw_rotation_join_membership_evidence ev USING(slot_id)
   WHERE b.slot_id=NEW.slot_id AND ROW(b.candidate_account_id,b.workspace_id,b.platform_workspace_id,b.secret_revision,b.subject_id)
    IS NOT DISTINCT FROM ROW(NEW.candidate_account_id,NEW.workspace_id,NEW.platform_workspace_id,NEW.secret_revision,NEW.subject_id)
   AND ev.reconciliation_attempt=NEW.membership_attempt AND ev.evidence_digest=NEW.membership_digest
   AND ev.result='confirmed' AND ev.platform_member_id=NEW.platform_member_id AND ev.observed_at>clock_timestamp()-interval '30 seconds'
   AND ev.reconciliation_attempt=(SELECT max(latest.reconciliation_attempt) FROM public.tsw_rotation_join_membership_evidence latest WHERE latest.slot_id=NEW.slot_id))
  THEN RAISE EXCEPTION 'credential attempt requires original confirmed binding'; END IF;
 ELSE
  IF ROW(NEW.slot_id,NEW.attempt_id,NEW.generation,NEW.candidate_account_id,NEW.workspace_id,NEW.platform_workspace_id,NEW.secret_revision,NEW.subject_id,NEW.membership_attempt,NEW.membership_digest,NEW.platform_member_id,NEW.created_at)
   IS DISTINCT FROM ROW(OLD.slot_id,OLD.attempt_id,OLD.generation,OLD.candidate_account_id,OLD.workspace_id,OLD.platform_workspace_id,OLD.secret_revision,OLD.subject_id,OLD.membership_attempt,OLD.membership_digest,OLD.platform_member_id,OLD.created_at)
  THEN RAISE EXCEPTION 'credential attempt binding is immutable'; END IF;
  IF NEW.lease_epoch=OLD.lease_epoch+1 THEN
   IF OLD.lease_expires_at>clock_timestamp() OR NEW.lease_owner IS NULL OR NEW.lease_token IS NOT DISTINCT FROM OLD.lease_token OR NEW.lease_expires_at<=clock_timestamp() OR
    NOT public.tsw_rotation_join_credential_authority(NEW.slot_id,NEW.owner_session)
    THEN RAISE EXCEPTION 'credential lease claim is stale'; END IF;
  ELSIF NEW.lease_epoch<>OLD.lease_epoch OR OLD.lease_owner IS NULL OR OLD.lease_expires_at<=clock_timestamp() OR NEW.lease_owner IS NOT NULL OR NEW.lease_token IS NOT NULL OR NEW.owner_session IS NOT NULL OR NEW.lease_expires_at IS NOT NULL THEN
   RAISE EXCEPTION 'credential lease permits only claim or release';
  END IF;
 END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER tsw_rotation_join_credential_attempt_guard BEFORE INSERT OR UPDATE OR DELETE ON public.tsw_rotation_join_credential_attempts FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_join_credential_attempt_guard();
-- +goose StatementBegin
CREATE FUNCTION public.tsw_rotation_join_credential_write_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public,pg_temp AS $$
DECLARE a public.tsw_rotation_join_credential_attempts; web public.tsw_rotation_join_credential_components; oauth public.tsw_rotation_join_credential_components;
BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'credential checkpoint is append-only'; END IF;
 SELECT * INTO a FROM public.tsw_rotation_join_credential_attempts WHERE attempt_id=NEW.attempt_id FOR UPDATE;
 IF a.attempt_id IS NULL OR ROW(a.lease_epoch,a.lease_owner,a.lease_token,a.owner_session) IS DISTINCT FROM ROW(NEW.lease_epoch,NEW.lease_owner,NEW.lease_token,NEW.owner_session)
 OR a.lease_expires_at<=clock_timestamp() OR NOT public.tsw_rotation_join_credential_authority(a.slot_id,NEW.owner_session)
 THEN RAISE EXCEPTION 'credential checkpoint requires current lease and original authority'; END IF;
 IF TG_TABLE_NAME='tsw_rotation_join_credential_events' THEN
  IF NEW.observed_at>clock_timestamp() OR NEW.observed_at<clock_timestamp()-interval '30 seconds' THEN RAISE EXCEPTION 'credential event stale'; END IF;
  IF NEW.event='started' AND (EXISTS(SELECT 1 FROM public.tsw_rotation_join_credential_generations WHERE attempt_id=a.attempt_id) OR
   (NEW.stage='oauth' AND NOT EXISTS(SELECT 1 FROM public.tsw_rotation_join_credential_components WHERE attempt_id=a.attempt_id AND kind='web')))
  THEN RAISE EXCEPTION 'credential stage ordering invalid'; END IF;
  IF NEW.event='review_required' AND NOT EXISTS(SELECT 1 FROM public.tsw_rotation_join_credential_events WHERE attempt_id=a.attempt_id AND stage=NEW.stage AND event='started') THEN RAISE EXCEPTION 'review requires original marker'; END IF;
 ELSIF TG_TABLE_NAME='tsw_rotation_join_credential_components' THEN
  IF NEW.observed_at>clock_timestamp() OR NEW.observed_at<clock_timestamp()-interval '30 seconds' OR NEW.expires_at<=clock_timestamp()+interval '1 minute' OR
   NOT EXISTS(SELECT 1 FROM public.tsw_rotation_join_credential_events WHERE attempt_id=a.attempt_id AND stage=NEW.kind AND event='started' AND lease_epoch=NEW.lease_epoch AND lease_owner=NEW.lease_owner AND lease_token=NEW.lease_token AND xmin::text::bigint<>mod(pg_current_xact_id()::text::numeric,4294967296)::bigint) OR
   EXISTS(SELECT 1 FROM public.tsw_rotation_join_credential_events WHERE attempt_id=a.attempt_id AND stage=NEW.kind AND event='review_required') OR
   NEW.sealed_digest<>encode(sha256(NEW.sealed),'hex')
  THEN RAISE EXCEPTION 'credential component requires exact original response checkpoint'; END IF;
 ELSE
  SELECT * INTO web FROM public.tsw_rotation_join_credential_components WHERE attempt_id=a.attempt_id AND kind='web';
  SELECT * INTO oauth FROM public.tsw_rotation_join_credential_components WHERE attempt_id=a.attempt_id AND kind='oauth';
  IF web.attempt_id IS NULL OR oauth.attempt_id IS NULL OR ROW(NEW.generation,NEW.candidate_account_id,NEW.workspace_id,NEW.web_digest,NEW.oauth_digest,NEW.membership_digest)
   IS DISTINCT FROM ROW(a.generation,a.candidate_account_id,a.workspace_id,web.sealed_digest,oauth.sealed_digest,a.membership_digest)
   OR NEW.verified_at>clock_timestamp() OR NEW.verified_at<clock_timestamp()-interval '30 seconds'
   OR NEW.expires_at<>LEAST(web.expires_at,oauth.expires_at) OR NEW.expires_at<=clock_timestamp()+interval '1 minute'
   OR EXISTS(SELECT 1 FROM public.tsw_rotation_join_credential_components c WHERE c.attempt_id=a.attempt_id AND c.xmin::text::bigint=mod(pg_current_xact_id()::text::numeric,4294967296)::bigint)
   OR EXISTS(SELECT 1 FROM public.tsw_rotation_join_credential_events WHERE attempt_id=a.attempt_id AND event='review_required')
   OR EXISTS(SELECT 1 FROM public.tsw_rotation_join_membership_evidence ev WHERE ev.slot_id=a.slot_id AND ev.reconciliation_attempt>a.membership_attempt AND (ev.result<>'confirmed' OR ev.platform_member_id<>a.platform_member_id))
  THEN RAISE EXCEPTION 'credential publication requires complete exact components'; END IF;
 END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER tsw_rotation_join_credential_event_guard BEFORE INSERT OR UPDATE OR DELETE ON public.tsw_rotation_join_credential_events FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_join_credential_write_guard();
CREATE TRIGGER tsw_rotation_join_credential_component_guard BEFORE INSERT OR UPDATE OR DELETE ON public.tsw_rotation_join_credential_components FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_join_credential_write_guard();
CREATE TRIGGER tsw_rotation_join_credential_generation_guard BEFORE INSERT OR UPDATE OR DELETE ON public.tsw_rotation_join_credential_generations FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_join_credential_write_guard();

-- +goose Down
DROP TABLE public.tsw_rotation_join_credential_generations;
DROP TABLE public.tsw_rotation_join_credential_components;
DROP TABLE public.tsw_rotation_join_credential_events;
DROP TABLE public.tsw_rotation_join_credential_attempts;
DROP FUNCTION public.tsw_rotation_join_credential_write_guard();
DROP FUNCTION public.tsw_rotation_join_credential_attempt_guard();
DROP FUNCTION public.tsw_rotation_join_credential_authority(uuid,uuid);
