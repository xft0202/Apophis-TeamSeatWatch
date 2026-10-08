-- +goose Up
-- These point lookups join immutable source facts by primary keys. Limit join
-- reordering to the written dependency order: exhaustive planning of their
-- nested authorization joins otherwise consumes the short qualification window
-- when a frozen batch contains more than 100 accounts. All predicates remain
-- unchanged, and the setting applies only while each function executes.
ALTER FUNCTION public.tsw_rotation_join_credential_authority(uuid,uuid) SET join_collapse_limit = 1;
ALTER FUNCTION public.tsw_archived_batch_zip_ready(uuid,uuid) SET join_collapse_limit = 1;

-- +goose Down
ALTER FUNCTION public.tsw_archived_batch_zip_ready(uuid,uuid) RESET join_collapse_limit;
ALTER FUNCTION public.tsw_rotation_join_credential_authority(uuid,uuid) RESET join_collapse_limit;
