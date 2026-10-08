-- +goose Up
CREATE INDEX tsw_target_accounts_email_domain ON tsw_target_accounts (split_part(identifier, '@', 2), identifier, id);
CREATE INDEX tsw_target_accounts_identifier_order ON tsw_target_accounts (identifier, id);
CREATE INDEX tsw_personal_probe_items_latest_target ON tsw_personal_probe_items (target_account_id, finished_at DESC)
 WHERE finished_at IS NOT NULL AND outcome IS NOT NULL;

-- +goose Down
DROP INDEX tsw_personal_probe_items_latest_target;
DROP INDEX tsw_target_accounts_identifier_order;
DROP INDEX tsw_target_accounts_email_domain;
