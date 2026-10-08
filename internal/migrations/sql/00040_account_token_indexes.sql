-- +goose Up
CREATE INDEX tsw_rotation_join_credential_attempts_account_idx
 ON tsw_rotation_join_credential_attempts(candidate_account_id,secret_revision,attempt_id);

-- +goose Down
DROP INDEX tsw_rotation_join_credential_attempts_account_idx;
