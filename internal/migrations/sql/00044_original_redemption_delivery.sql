-- +goose Up
-- The first customer access token is minted in the same transaction as its
-- order. Only this exact evidence can recover an existing original delivery.
ALTER TABLE tsw_orders ADD COLUMN original_delivery_version_id uuid;
ALTER TABLE tsw_orders ADD CONSTRAINT tsw_orders_original_delivery_fk
    FOREIGN KEY (original_delivery_version_id, oauth_asset_id)
    REFERENCES tsw_delivery_versions(id, oauth_asset_id) ON DELETE RESTRICT;
UPDATE tsw_orders ord SET original_delivery_version_id=evidence.delivery_version_id
FROM (
    SELECT token.order_id, (array_agg(DISTINCT token.delivery_version_id))[1] AS delivery_version_id
    FROM tsw_public_tokens token JOIN tsw_orders ord ON ord.id=token.order_id
    WHERE token.token_kind='customer_access' AND token.issued_at=ord.created_at
    GROUP BY token.order_id HAVING count(DISTINCT token.delivery_version_id)=1
) evidence WHERE evidence.order_id=ord.id;

-- +goose StatementBegin
CREATE FUNCTION tsw_orders_original_delivery_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.original_delivery_version_id IS DISTINCT FROM OLD.original_delivery_version_id THEN
        RAISE EXCEPTION 'original redemption delivery is immutable';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER tsw_orders_original_delivery_immutable_trigger
    BEFORE UPDATE OF original_delivery_version_id ON tsw_orders
    FOR EACH ROW EXECUTE FUNCTION tsw_orders_original_delivery_immutable();

-- +goose Down
DROP TRIGGER tsw_orders_original_delivery_immutable_trigger ON tsw_orders;
DROP FUNCTION tsw_orders_original_delivery_immutable();
ALTER TABLE tsw_orders DROP COLUMN original_delivery_version_id;
