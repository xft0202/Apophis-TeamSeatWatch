-- +goose Up
-- Owner-managed delivery destinations. Secrets are deployment-key encrypted and
-- never projected through the Owner API. This registry is intentionally separate
-- from delivery history; changing a destination cannot rewrite a frozen operation.
CREATE TABLE tsw_delivery_destinations (
    id uuid CONSTRAINT tsw_delivery_destinations_pk PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL CONSTRAINT tsw_delivery_destinations_name_ck CHECK (length(name) BETWEEN 1 AND 120),
    endpoint text NOT NULL CONSTRAINT tsw_delivery_destinations_endpoint_ck CHECK (length(endpoint) BETWEEN 1 AND 2048),
    target_group text NOT NULL CONSTRAINT tsw_delivery_destinations_target_group_ck CHECK (target_group ~ '^[1-9][0-9]{0,18}$' AND target_group::numeric <= 9223372036854775807),
    secret_key_version smallint NOT NULL CONSTRAINT tsw_delivery_destinations_secret_version_ck CHECK (secret_key_version > 0),
    secret_nonce bytea NOT NULL CONSTRAINT tsw_delivery_destinations_secret_nonce_ck CHECK (octet_length(secret_nonce) = 12),
    secret_ciphertext bytea NOT NULL CONSTRAINT tsw_delivery_destinations_secret_ciphertext_ck CHECK (octet_length(secret_ciphertext) > 16),
    enabled boolean NOT NULL DEFAULT true,
    revision bigint NOT NULL DEFAULT 1 CONSTRAINT tsw_delivery_destinations_revision_ck CHECK (revision > 0),
    test_connection text,
    test_target text,
    test_revision bigint,
    tested_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT tsw_delivery_destinations_test_connection_ck CHECK (test_connection IS NULL OR test_connection IN ('connected','connection_failed','permission_denied')),
    CONSTRAINT tsw_delivery_destinations_test_target_ck CHECK (test_target IS NULL OR test_target IN ('connected','target_mismatch','permission_denied','untested')),
    CONSTRAINT tsw_delivery_destinations_test_shape_ck CHECK ((test_connection IS NULL AND test_target IS NULL AND test_revision IS NULL AND tested_at IS NULL) OR (test_connection IS NOT NULL AND test_target IS NOT NULL AND test_revision IS NOT NULL AND tested_at IS NOT NULL))
);
CREATE TABLE tsw_delivery_destination_selection (
    singleton boolean PRIMARY KEY DEFAULT true CONSTRAINT tsw_delivery_destination_selection_singleton_ck CHECK (singleton),
    destination_id uuid NOT NULL CONSTRAINT tsw_delivery_destination_selection_destination_fk REFERENCES tsw_delivery_destinations(id) ON DELETE RESTRICT,
    selected_at timestamptz NOT NULL DEFAULT now()
);
REVOKE ALL ON tsw_delivery_destinations, tsw_delivery_destination_selection FROM PUBLIC;

-- +goose Down
REVOKE ALL ON tsw_delivery_destinations, tsw_delivery_destination_selection FROM PUBLIC;
DROP TABLE tsw_delivery_destination_selection;
DROP TABLE tsw_delivery_destinations;
