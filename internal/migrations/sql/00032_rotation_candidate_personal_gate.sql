-- +goose Up
-- Personal refresh is credential-generation churn, not renewal of the frozen
-- authorization. Fence both reservation/deletion and publication without
-- advancing the target's original source epoch.
-- +goose StatementBegin
CREATE FUNCTION public.tsw_rotation_candidate_personal_gate() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public,pg_temp AS $$
DECLARE old_id uuid; new_id uuid; affected record;
BEGIN
 IF TG_OP<>'INSERT' THEN old_id=OLD.target_account_id; END IF;
 IF TG_OP<>'DELETE' THEN new_id=NEW.target_account_id; END IF;
 FOR affected IN SELECT DISTINCT id FROM (VALUES(old_id),(new_id)) AS ids(id) WHERE id IS NOT NULL ORDER BY id LOOP
  PERFORM pg_advisory_xact_lock(hashtextextended('tsw.rotation.action.target_account/'||affected.id::text,0));
 END LOOP;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER tsw_rotation_candidate_personal_gate BEFORE INSERT OR UPDATE OR DELETE ON public.tsw_target_personal_access FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_candidate_personal_gate();
CREATE TRIGGER tsw_rotation_candidate_personal_gate BEFORE INSERT OR UPDATE OR DELETE ON public.tsw_target_personal_sessions FOR EACH ROW EXECUTE FUNCTION public.tsw_rotation_candidate_personal_gate();

-- +goose Down
DROP TRIGGER tsw_rotation_candidate_personal_gate ON public.tsw_target_personal_sessions;
DROP TRIGGER tsw_rotation_candidate_personal_gate ON public.tsw_target_personal_access;
DROP FUNCTION public.tsw_rotation_candidate_personal_gate();
