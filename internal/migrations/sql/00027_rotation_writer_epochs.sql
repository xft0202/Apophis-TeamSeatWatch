-- +goose Up
-- FOUNDATION ONLY. No confirmation/action authorization consumes these versions yet.
-- Rows are created with their stable parent and retained if the parent is removed;
-- never manufacture a missing epoch while reading facts.
CREATE TABLE tsw_rotation_epochs (
 kind text NOT NULL CHECK (kind IN ('owner','workspace','mother','target_account','standby_batch','destination')),
 id uuid NOT NULL,
 version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
 PRIMARY KEY (kind,id)
);
REVOKE ALL ON tsw_rotation_epochs FROM PUBLIC;
-- +goose StatementBegin
CREATE FUNCTION tsw_rotation_epoch_advance(scope_kind text, scope_id uuid) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
 IF scope_id IS NULL THEN RETURN; END IF;
 UPDATE tsw_rotation_epochs SET version=version+1 WHERE kind=scope_kind AND id=scope_id;
 IF NOT FOUND THEN RAISE EXCEPTION 'missing rotation epoch: % %',scope_kind,scope_id; END IF;
END;
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION tsw_rotation_epoch_parent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE scope_kind text := TG_ARGV[0]; old_id uuid; new_id uuid;
BEGIN
 IF TG_OP <> 'INSERT' THEN old_id := (to_jsonb(OLD)->>'id')::uuid; END IF;
 IF TG_OP <> 'DELETE' THEN new_id := (to_jsonb(NEW)->>'id')::uuid; END IF;
 IF TG_OP = 'INSERT' OR (TG_OP = 'UPDATE' AND old_id IS DISTINCT FROM new_id) THEN
   INSERT INTO tsw_rotation_epochs(kind,id) VALUES(scope_kind,new_id);
 END IF;
 IF TG_OP <> 'INSERT' THEN PERFORM tsw_rotation_epoch_advance(scope_kind,old_id); END IF;
 IF TG_OP = 'UPDATE' AND new_id IS DISTINCT FROM old_id THEN PERFORM tsw_rotation_epoch_advance(scope_kind,new_id); END IF;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
-- Owner and Workspace payload updates are not rotationFacts inputs. Only
-- their existence matters to these two stable scope keys.
CREATE TRIGGER tsw_rotation_epoch_parent AFTER INSERT OR DELETE ON tsw_owners FOR EACH ROW EXECUTE FUNCTION tsw_rotation_epoch_parent('owner');
CREATE TRIGGER tsw_rotation_epoch_parent AFTER INSERT OR DELETE ON tsw_workspaces FOR EACH ROW EXECUTE FUNCTION tsw_rotation_epoch_parent('workspace');
CREATE TRIGGER tsw_rotation_epoch_parent AFTER INSERT OR UPDATE OR DELETE ON tsw_mother_accounts FOR EACH ROW EXECUTE FUNCTION tsw_rotation_epoch_parent('mother');
CREATE TRIGGER tsw_rotation_epoch_parent AFTER INSERT OR UPDATE OR DELETE ON tsw_target_accounts FOR EACH ROW EXECUTE FUNCTION tsw_rotation_epoch_parent('target_account');
CREATE TRIGGER tsw_rotation_epoch_parent AFTER INSERT OR UPDATE OR DELETE ON tsw_standby_child_batches FOR EACH ROW EXECUTE FUNCTION tsw_rotation_epoch_parent('standby_batch');
CREATE TRIGGER tsw_rotation_epoch_parent AFTER INSERT OR UPDATE OR DELETE ON tsw_delivery_destinations FOR EACH ROW EXECUTE FUNCTION tsw_rotation_epoch_parent('destination');
-- Each fact mutation advances its stable scope, including both keys when a
-- covered FK is reassigned. Sorting does not order pre-existing multi-row locks.
-- +goose StatementBegin
CREATE FUNCTION tsw_rotation_epoch_fact() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE old_id uuid; new_id uuid; scope_kind text := TG_ARGV[0]; scope_column text := TG_ARGV[1]; key record;
BEGIN
 IF TG_OP <> 'INSERT' THEN
   old_id := (to_jsonb(OLD)->>scope_column)::uuid;
 END IF;
 IF TG_OP <> 'DELETE' THEN
   new_id := (to_jsonb(NEW)->>scope_column)::uuid;
 END IF;
 FOR key IN SELECT DISTINCT id FROM (VALUES (old_id),(new_id)) AS affected(id) WHERE id IS NOT NULL ORDER BY id LOOP
   PERFORM tsw_rotation_epoch_advance(scope_kind,key.id);
 END LOOP;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER tsw_rotation_epoch_fact AFTER INSERT OR UPDATE OR DELETE ON tsw_operation_selection_drafts FOR EACH ROW EXECUTE FUNCTION tsw_rotation_epoch_fact('owner','owner_id');
CREATE TRIGGER tsw_rotation_epoch_fact AFTER INSERT OR UPDATE OR DELETE ON tsw_mother_account_credentials FOR EACH ROW EXECUTE FUNCTION tsw_rotation_epoch_fact('mother','mother_account_id');
CREATE TRIGGER tsw_rotation_epoch_fact AFTER INSERT OR UPDATE OR DELETE ON tsw_mother_discoveries FOR EACH ROW EXECUTE FUNCTION tsw_rotation_epoch_fact('mother','mother_account_id');
CREATE TRIGGER tsw_rotation_epoch_fact AFTER INSERT OR UPDATE OR DELETE ON tsw_mother_personal_sessions FOR EACH ROW EXECUTE FUNCTION tsw_rotation_epoch_fact('mother','mother_account_id');
CREATE TRIGGER tsw_rotation_epoch_fact AFTER INSERT OR UPDATE OR DELETE ON tsw_mother_workspace_visibility FOR EACH ROW EXECUTE FUNCTION tsw_rotation_epoch_fact('workspace','workspace_id');
CREATE TRIGGER tsw_rotation_epoch_fact AFTER INSERT OR UPDATE OR DELETE ON tsw_selected_workspace_tokens FOR EACH ROW EXECUTE FUNCTION tsw_rotation_epoch_fact('workspace','workspace_id');
CREATE TRIGGER tsw_rotation_epoch_fact AFTER INSERT OR UPDATE OR DELETE ON tsw_workspace_verifications FOR EACH ROW EXECUTE FUNCTION tsw_rotation_epoch_fact('workspace','workspace_id');
CREATE TRIGGER tsw_rotation_epoch_fact AFTER INSERT OR UPDATE OR DELETE ON tsw_target_credentials FOR EACH ROW EXECUTE FUNCTION tsw_rotation_epoch_fact('target_account','target_account_id');
-- Standby membership itself is deliberately NOT triggered here. The existing
-- editor advances old/new batch versions, which the batch parent trigger fences.
-- Combining its account-key trigger with a batch-key trigger would invert the
-- editor's account-before-batch lock order. Direct SQL membership edits remain
-- an uncovered writer; this schema is not an authorization fence.
CREATE TRIGGER tsw_rotation_epoch_fact AFTER INSERT OR UPDATE OR DELETE ON tsw_batch_memberships FOR EACH ROW EXECUTE FUNCTION tsw_rotation_epoch_fact('target_account','target_account_id');
CREATE TRIGGER tsw_rotation_epoch_fact AFTER INSERT OR UPDATE OR DELETE ON tsw_expiry_rotation_previews FOR EACH ROW EXECUTE FUNCTION tsw_rotation_epoch_fact('workspace','workspace_id');

-- These two relationship hops also handle first-ever delivery in *another*
-- Workspace. The account key is not derived from an existing delivery row.
-- +goose StatementBegin
CREATE FUNCTION tsw_rotation_epoch_relation() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE old_id uuid; new_id uuid; key record;
BEGIN
 IF TG_TABLE_NAME = 'tsw_workspace_verification_entries' THEN
   IF TG_OP <> 'INSERT' THEN SELECT workspace_id INTO STRICT old_id FROM tsw_workspace_verifications WHERE id=OLD.verification_id; END IF;
   IF TG_OP <> 'DELETE' THEN SELECT workspace_id INTO STRICT new_id FROM tsw_workspace_verifications WHERE id=NEW.verification_id; END IF;
 ELSIF TG_TABLE_NAME = 'tsw_oauth_assets' THEN
   IF TG_OP <> 'INSERT' THEN SELECT target_account_id INTO STRICT old_id FROM tsw_batch_memberships WHERE id=OLD.membership_id; END IF;
   IF TG_OP <> 'DELETE' THEN SELECT target_account_id INTO STRICT new_id FROM tsw_batch_memberships WHERE id=NEW.membership_id; END IF;
 ELSE
   IF TG_OP <> 'INSERT' THEN SELECT m.target_account_id INTO STRICT old_id FROM tsw_oauth_assets a JOIN tsw_batch_memberships m ON m.id=a.membership_id WHERE a.id=OLD.oauth_asset_id; END IF;
   IF TG_OP <> 'DELETE' THEN SELECT m.target_account_id INTO STRICT new_id FROM tsw_oauth_assets a JOIN tsw_batch_memberships m ON m.id=a.membership_id WHERE a.id=NEW.oauth_asset_id; END IF;
 END IF;
 FOR key IN SELECT DISTINCT id FROM (VALUES (old_id),(new_id)) AS affected(id) WHERE id IS NOT NULL ORDER BY id LOOP
   PERFORM tsw_rotation_epoch_advance(TG_ARGV[0],key.id);
 END LOOP;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER tsw_rotation_epoch_relation AFTER INSERT OR UPDATE OR DELETE ON tsw_workspace_verification_entries FOR EACH ROW EXECUTE FUNCTION tsw_rotation_epoch_relation('workspace');
-- An asset without delivery can be deleted without changing the delivery
-- predicate. A delivered asset cannot be deleted (delivery immutability guard).
CREATE TRIGGER tsw_rotation_epoch_relation AFTER INSERT OR UPDATE ON tsw_oauth_assets FOR EACH ROW EXECUTE FUNCTION tsw_rotation_epoch_relation('target_account');
-- Delivery versions are immutable after insertion.
CREATE TRIGGER tsw_rotation_epoch_relation AFTER INSERT ON tsw_delivery_versions FOR EACH ROW EXECUTE FUNCTION tsw_rotation_epoch_relation('target_account');
-- The 00022 guard accessed OLD.outcome even for an entry DELETE because its
-- compound IF was evaluated against the entry row. Separate trigger branches
-- so expired entries can be deleted before their parent verification.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION tsw_workspace_verification_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'UPDATE' THEN
   IF TG_TABLE_NAME = 'tsw_workspace_verifications' THEN
     IF OLD.outcome = 'verifying' AND NEW.outcome IN ('verified','partial','failed','permission_denied') AND
        (NEW.id,NEW.workspace_id,NEW.mother_account_id,NEW.discovery_run_id,NEW.session_generation,NEW.secret_revision,NEW.token_attempt,NEW.token_exchange_id,NEW.source) =
        (OLD.id,OLD.workspace_id,OLD.mother_account_id,OLD.discovery_run_id,OLD.session_generation,OLD.secret_revision,OLD.token_attempt,OLD.token_exchange_id,OLD.source) THEN RETURN NEW; END IF;
   END IF;
 ELSIF TG_OP = 'DELETE' THEN
   IF TG_TABLE_NAME = 'tsw_workspace_verifications' THEN
     IF OLD.expires_at <= now() THEN RETURN OLD; END IF;
   ELSIF TG_TABLE_NAME = 'tsw_workspace_verification_entries' THEN
     IF EXISTS (SELECT 1 FROM tsw_workspace_verifications WHERE id=OLD.verification_id AND expires_at <= now()) THEN RETURN OLD; END IF;
   END IF;
 END IF;
 RAISE EXCEPTION 'workspace verification is append-only until expiry';
END;
$$;
-- +goose StatementEnd
-- Goose runs this migration transactionally. Install all hooks first, then
-- backfill immediately before commit; concurrent DML sees both at once.
INSERT INTO tsw_rotation_epochs(kind,id)
 SELECT 'owner',id FROM tsw_owners UNION ALL
 SELECT 'workspace',id FROM tsw_workspaces UNION ALL
 SELECT 'mother',id FROM tsw_mother_accounts UNION ALL
 SELECT 'target_account',id FROM tsw_target_accounts UNION ALL
 SELECT 'standby_batch',id FROM tsw_standby_child_batches UNION ALL
 SELECT 'destination',id FROM tsw_delivery_destinations;

-- +goose Down
-- Restore the original 00022 guard on downgrade.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION tsw_workspace_verification_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
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
DROP TRIGGER tsw_rotation_epoch_relation ON tsw_delivery_versions;
DROP TRIGGER tsw_rotation_epoch_relation ON tsw_oauth_assets;
DROP TRIGGER tsw_rotation_epoch_relation ON tsw_workspace_verification_entries;
DROP FUNCTION tsw_rotation_epoch_relation();
DROP TRIGGER tsw_rotation_epoch_fact ON tsw_expiry_rotation_previews;
DROP TRIGGER tsw_rotation_epoch_fact ON tsw_batch_memberships;
DROP TRIGGER tsw_rotation_epoch_fact ON tsw_target_credentials;
DROP TRIGGER tsw_rotation_epoch_fact ON tsw_workspace_verifications;
DROP TRIGGER tsw_rotation_epoch_fact ON tsw_selected_workspace_tokens;
DROP TRIGGER tsw_rotation_epoch_fact ON tsw_mother_workspace_visibility;
DROP TRIGGER tsw_rotation_epoch_fact ON tsw_mother_personal_sessions;
DROP TRIGGER tsw_rotation_epoch_fact ON tsw_mother_discoveries;
DROP TRIGGER tsw_rotation_epoch_fact ON tsw_mother_account_credentials;
DROP TRIGGER tsw_rotation_epoch_fact ON tsw_operation_selection_drafts;
DROP FUNCTION tsw_rotation_epoch_fact();
DROP TRIGGER tsw_rotation_epoch_parent ON tsw_delivery_destinations;
DROP TRIGGER tsw_rotation_epoch_parent ON tsw_standby_child_batches;
DROP TRIGGER tsw_rotation_epoch_parent ON tsw_target_accounts;
DROP TRIGGER tsw_rotation_epoch_parent ON tsw_mother_accounts;
DROP TRIGGER tsw_rotation_epoch_parent ON tsw_workspaces;
DROP TRIGGER tsw_rotation_epoch_parent ON tsw_owners;
DROP FUNCTION tsw_rotation_epoch_parent();
DROP FUNCTION tsw_rotation_epoch_advance(text,uuid);
DROP TABLE tsw_rotation_epochs;
