-- +goose Up
-- Derived original obligations only: never advance or adopt source epochs.
CREATE TABLE public.tsw_rotation_candidate_join_intents (
 slot_id uuid PRIMARY KEY REFERENCES public.tsw_rotation_removal_slots(id) ON DELETE RESTRICT,
 preview_id uuid NOT NULL REFERENCES public.tsw_expiry_rotation_previews(id) ON DELETE RESTRICT,
 workspace_id uuid NOT NULL REFERENCES public.tsw_workspaces(id) ON DELETE RESTRICT,
 owner_id uuid NOT NULL REFERENCES public.tsw_owners(id) ON DELETE RESTRICT,
 candidate_account_id uuid NOT NULL REFERENCES public.tsw_target_accounts(id) ON DELETE RESTRICT,
 original_platform_member_id text NOT NULL CHECK(length(original_platform_member_id) BETWEEN 1 AND 255),
 candidate_identifier text NOT NULL CHECK(length(candidate_identifier) BETWEEN 1 AND 254),
 seat_type text NOT NULL CHECK(seat_type='prolite'),
 authorization_digest text NOT NULL CHECK(length(authorization_digest)=64),
 authorized_session uuid NOT NULL REFERENCES public.tsw_owner_sessions(id) ON DELETE RESTRICT,
 epoch_versions jsonb NOT NULL CHECK(jsonb_typeof(epoch_versions)='object'),
 released_verification_id uuid NOT NULL REFERENCES public.tsw_rotation_removal_evidence(id) ON DELETE RESTRICT,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(preview_id,candidate_account_id)
);
REVOKE ALL ON public.tsw_rotation_candidate_join_intents FROM PUBLIC;
-- +goose StatementBegin
CREATE FUNCTION public.tsw_rotation_join_intent_guard() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public,pg_temp AS $$
BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'rotation candidate join intent is immutable'; END IF;
 IF NOT EXISTS (
  SELECT 1 FROM public.tsw_rotation_released_slots s
  JOIN public.tsw_rotation_removals r ON r.preview_id=s.preview_id
  JOIN public.tsw_expiry_rotation_previews p ON p.id=s.preview_id
  JOIN public.tsw_rotation_removal_evidence e ON e.id=s.verification_id
  WHERE s.id=NEW.slot_id AND s.preview_id=NEW.preview_id AND s.workspace_id=NEW.workspace_id
   AND r.owner_id=NEW.owner_id AND p.owner_id=NEW.owner_id AND p.workspace_id=NEW.workspace_id
   AND s.candidate_account_id=NEW.candidate_account_id AND s.platform_member_id=NEW.original_platform_member_id
   AND s.seat_type=NEW.seat_type AND r.authorization_digest=NEW.authorization_digest
   AND p.authorization_digest=NEW.authorization_digest AND p.authorized_session::uuid=NEW.authorized_session
   AND p.epoch_versions=NEW.epoch_versions AND s.verification_id=NEW.released_verification_id
   AND e.slot_id=s.id AND e.lease_epoch=s.lease_epoch AND e.authorization_digest=NEW.authorization_digest
   AND e.observed_at<=clock_timestamp()
   AND EXISTS(SELECT 1 FROM jsonb_array_elements(p.assignments) a WHERE a->>'accountId'=NEW.candidate_account_id::text AND a->>'platformMemberId'=NEW.original_platform_member_id)
   AND EXISTS(SELECT 1 FROM jsonb_array_elements(p.facts->'candidates') c WHERE c->>'accountId'=NEW.candidate_account_id::text AND c->>'identifier'=NEW.candidate_identifier AND c->>'decision'='eligible' AND c->>'seatType'=NEW.seat_type AND c->>'deliveryStatus'='join_candidate_pending_first_probe')
   AND EXISTS(SELECT 1 FROM jsonb_array_elements(p.facts->'slots') original WHERE original->>'platformMemberId'=NEW.original_platform_member_id AND original->>'accountId'=s.original_account_id::text AND original->>'identifier'=s.identifier AND original->>'decision'='replaceable' AND original->>'seatType'=NEW.seat_type)
 ) THEN RAISE EXCEPTION 'original authorization and fresh released slot required'; END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER tsw_rotation_join_intent_guard BEFORE INSERT OR UPDATE OR DELETE ON public.tsw_rotation_candidate_join_intents FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_join_intent_guard();

-- +goose Down
DROP TABLE public.tsw_rotation_candidate_join_intents;
DROP FUNCTION public.tsw_rotation_join_intent_guard();
