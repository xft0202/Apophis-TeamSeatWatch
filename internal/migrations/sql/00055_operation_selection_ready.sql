-- +goose Up
ALTER TABLE tsw_operation_selection_drafts DROP CONSTRAINT tsw_operation_selection_drafts_step_check;
UPDATE tsw_operation_selection_drafts SET step='ready' WHERE step IN ('destination','complete');
ALTER TABLE tsw_operation_selection_drafts ADD CONSTRAINT tsw_operation_selection_drafts_step_check CHECK (step IN ('mother','workspace','children','ready'));
-- Existing destination snapshots remain historical data; the current selection
-- contract and execution flow no longer read or write them.

-- +goose Down
ALTER TABLE tsw_operation_selection_drafts DROP CONSTRAINT tsw_operation_selection_drafts_step_check;
UPDATE tsw_operation_selection_drafts SET step='destination' WHERE step='ready';
ALTER TABLE tsw_operation_selection_drafts ADD CONSTRAINT tsw_operation_selection_drafts_step_check CHECK (step IN ('mother','workspace','children','destination','complete'));
