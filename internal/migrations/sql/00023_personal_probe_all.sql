-- +goose Up
-- Personal probes are separate from legacy password-backed target_account_probe tasks.
CREATE TABLE tsw_personal_probe_batches (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 owner_id uuid NOT NULL REFERENCES tsw_owners(id),
 request_key text NOT NULL,
 scope_hash text NOT NULL,
 scope_key_version smallint NOT NULL,
 confirmed_scope_token text NOT NULL,
 scope text NOT NULL CHECK (scope IN ('selected','filtered')),
 scope_label text NOT NULL,
 total integer NOT NULL CHECK (total > 0),
 canceled_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 CONSTRAINT tsw_personal_probe_batches_request_key_uq UNIQUE (owner_id,request_key)
);
CREATE TABLE tsw_personal_probe_items (
 batch_id uuid NOT NULL REFERENCES tsw_personal_probe_batches(id) ON DELETE CASCADE,
 target_account_id uuid NOT NULL REFERENCES tsw_target_accounts(id) ON DELETE CASCADE,
 identifier text NOT NULL,
 status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','succeeded','failed','canceled')),
 outcome text CHECK (outcome IN ('available','missing_personal_credential','credential_invalid','forbidden','banned','network_error','unknown')),
 endpoint text,
 http_status integer CHECK (http_status BETWEEN 100 AND 599),
 evidence_code text,
 verified_evidence boolean NOT NULL DEFAULT false,
 attempt_count integer NOT NULL DEFAULT 0,
 started_at timestamptz,
 finished_at timestamptz,
 CONSTRAINT tsw_personal_probe_ban_evidence_ck CHECK (
   outcome IS DISTINCT FROM 'banned' OR
   (verified_evidence AND endpoint='personal_usage' AND http_status=403 AND evidence_code='account_deactivated')
 ),
 CONSTRAINT tsw_personal_probe_available_evidence_ck CHECK (
   outcome IS DISTINCT FROM 'available' OR verified_evidence
 ),
 PRIMARY KEY (batch_id,target_account_id)
);
CREATE INDEX tsw_personal_probe_items_pending ON tsw_personal_probe_items(batch_id) WHERE status='queued';
-- +goose Down
DROP TABLE tsw_personal_probe_items;
DROP TABLE tsw_personal_probe_batches;
