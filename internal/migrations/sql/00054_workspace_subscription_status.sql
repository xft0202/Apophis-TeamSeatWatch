-- +goose Up
-- Historical expiry dates do not establish subscription billing health.
ALTER TABLE tsw_workspace_verifications ADD COLUMN subscription_status text NOT NULL
    DEFAULT 'unknown' CHECK (subscription_status IN ('active','delinquent','inactive','expired','unknown'));
ALTER TABLE tsw_workspace_verifications DROP CONSTRAINT tsw_workspace_verifications_check2;
ALTER TABLE tsw_workspace_verifications ADD CONSTRAINT tsw_workspace_verifications_summary_evidence CHECK (
    outcome = 'verified' OR (seat_limit IS NULL AND member_count IS NULL AND pending_invite_count IS NULL AND
        (active_until IS NULL OR (outcome = 'partial' AND sources @> '[{"source":"subscriptions","permission":"read","completeness":"complete","outcome":"operational"}]'::jsonb)))
);
ALTER TABLE tsw_workspace_verifications ADD CONSTRAINT tsw_workspace_verifications_subscription_evidence CHECK (
    subscription_status = 'unknown' OR (outcome IN ('verified','partial') AND
        sources @> '[{"source":"subscriptions","permission":"read","completeness":"complete","outcome":"operational"}]'::jsonb)
);

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Recorded subscription evidence cannot be discarded by rollback'; END $$;
-- +goose StatementEnd
