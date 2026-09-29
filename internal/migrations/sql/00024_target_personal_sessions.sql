-- +goose Up
-- Child Personal Web generations are not Workspace delivery tokens or target passwords.
CREATE TABLE tsw_target_personal_access (
 target_account_id uuid PRIMARY KEY REFERENCES tsw_target_accounts(id) ON DELETE CASCADE,
 secret_revision bigint NOT NULL CHECK (secret_revision > 0),
 attempt bigint NOT NULL DEFAULT 1 CHECK (attempt > 0),
 status text NOT NULL CHECK (status IN ('verifying','ready','invalid_login','missing_credentials','refresh_failed','unavailable')),
 checked_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE tsw_target_personal_sessions (
 target_account_id uuid PRIMARY KEY REFERENCES tsw_target_accounts(id) ON DELETE CASCADE,
 secret_revision bigint NOT NULL CHECK (secret_revision > 0),
 attempt bigint NOT NULL CHECK (attempt > 0),
 generation uuid NOT NULL,
 key_version smallint NOT NULL CHECK (key_version > 0),
 nonce bytea NOT NULL CHECK (octet_length(nonce) = 12),
 sealed_session bytea NOT NULL CHECK (octet_length(sealed_session) > 16),
 expires_at timestamptz NOT NULL,
 verified_at timestamptz NOT NULL DEFAULT now()
);
REVOKE ALL ON tsw_target_personal_access, tsw_target_personal_sessions FROM PUBLIC;

-- Invalidate in the credential/status transaction even if another action is in flight.
-- +goose StatementBegin
CREATE FUNCTION tsw_invalidate_target_personal() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target_id uuid;
BEGIN
 IF TG_TABLE_NAME = 'tsw_target_credentials' THEN
  IF NEW.secret_revision = OLD.secret_revision AND NEW.material_status = OLD.material_status AND NEW.platform_subject_id IS NOT DISTINCT FROM OLD.platform_subject_id THEN RETURN NEW; END IF;
  target_id := NEW.target_account_id;
 ELSE
  IF NEW.status = 'active' OR OLD.status = NEW.status THEN RETURN NEW; END IF;
  target_id := NEW.id;
 END IF;
 UPDATE tsw_target_personal_access SET attempt=attempt+1,status='refresh_failed',checked_at=now() WHERE target_account_id=target_id;
 DELETE FROM tsw_target_personal_sessions WHERE target_account_id = target_id;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER tsw_target_personal_credentials_invalidate AFTER UPDATE ON tsw_target_credentials FOR EACH ROW EXECUTE FUNCTION tsw_invalidate_target_personal();
CREATE TRIGGER tsw_target_personal_status_invalidate AFTER UPDATE ON tsw_target_accounts FOR EACH ROW EXECUTE FUNCTION tsw_invalidate_target_personal();

-- +goose Down
DROP TRIGGER tsw_target_personal_status_invalidate ON tsw_target_accounts;
DROP TRIGGER tsw_target_personal_credentials_invalidate ON tsw_target_credentials;
DROP FUNCTION tsw_invalidate_target_personal();
DROP TABLE tsw_target_personal_sessions;
DROP TABLE tsw_target_personal_access;
