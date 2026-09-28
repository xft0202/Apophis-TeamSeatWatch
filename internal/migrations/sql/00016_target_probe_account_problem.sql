-- +goose Up
-- Preserve a generic HTTP 403 as an account problem, not invalid credentials.
ALTER TABLE tsw_target_credentials DROP CONSTRAINT tsw_target_credentials_probe_status_ck;
ALTER TABLE tsw_target_credentials ADD CONSTRAINT tsw_target_credentials_probe_status_ck
    CHECK (latest_probe_status IS NULL OR latest_probe_status IN ('available', 'credential_invalid', 'account_problem', 'definitely_unavailable', 'transient_failure', 'unknown'));
ALTER TABLE tsw_operation_targets DROP CONSTRAINT tsw_operation_targets_preflight_status_ck;
ALTER TABLE tsw_operation_targets ADD CONSTRAINT tsw_operation_targets_preflight_status_ck
    CHECK (preflight_status IN ('pending', 'available', 'credential_invalid', 'account_problem', 'definitely_unavailable', 'transient_failure', 'unknown'));
UPDATE tsw_target_credentials SET latest_probe_status='account_problem',version=version+1
    WHERE latest_probe_status='credential_invalid' AND latest_probe_http_status=403;
UPDATE tsw_operation_targets SET preflight_status='account_problem',version=version+1
    WHERE preflight_status='credential_invalid' AND preflight_http_status=403;
-- An exchange can never prove membership availability. Recompute from a
-- relevant observation on the next read instead of retaining that projection.
UPDATE tsw_workspace_projections projection
    SET operational_state='unknown',conclusion_observation_id=NULL,evidence_expires_at=NULL,
        updated_at=now(),version=version+1
    FROM tsw_workspace_observations conclusion
    WHERE projection.conclusion_observation_id=conclusion.id
      AND projection.operational_state='operational'
      AND conclusion.source_endpoint='workspace_exchange';

-- +goose Down
-- This classification and invalidation are intentionally not reversible.
SELECT 1;
