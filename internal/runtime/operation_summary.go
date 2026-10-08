package runtime

// Compute execution facts only for projected batches. Pagination counts do not
// need to load every operation's targets, credentials or task history.
const batchExecutionSummarySQL = `(SELECT jsonb_build_object(
 'activeTaskCount',tasks.active_count,
 'invitationConfirmedCount',invitations.confirmed_count,
 'invitationFailedCount',invitations.failed_count,
 'invitationUncertainCount',invitations.uncertain_count,
 'invitationWaitingSeatCount',invitations.waiting_count,
 'readyCount',deliveries.ready_count,'cardCount',deliveries.card_count)
 FROM (SELECT count(*) FILTER(WHERE t.invitation_confirmed_at IS NOT NULL) AS confirmed_count,
   count(*) FILTER(WHERE t.invitation_confirmed_at IS NULL AND COALESCE(t.diagnostic_code,'')<>'premium_capacity_exceeded' AND (t.status='failed' OR t.status IN ('blocked','unknown') AND NOT t.platform_request_may_have_reached)) AS failed_count,
   count(*) FILTER(WHERE t.invitation_confirmed_at IS NULL AND t.status IN ('blocked','unknown') AND t.platform_request_may_have_reached) AS uncertain_count,
   count(*) FILTER(WHERE t.invitation_confirmed_at IS NULL AND t.diagnostic_code='premium_capacity_exceeded') AS waiting_count
   FROM tsw_operations op JOIN tsw_operation_targets t ON t.operation_id=op.id WHERE op.batch_id=batch.id AND op.operation_type='join') invitations
 CROSS JOIN (SELECT count(*) FILTER(WHERE a.status='ready') AS ready_count,count(c.id) AS card_count
   FROM tsw_batch_memberships m LEFT JOIN tsw_oauth_assets a ON a.membership_id=m.id LEFT JOIN tsw_cards c ON c.membership_id=m.id WHERE m.batch_id=batch.id) deliveries
 CROSS JOIN (SELECT count(*) AS active_count FROM tsw_tasks q WHERE q.status IN ('queued','retry_wait','running') AND (
   q.operation_target_id IN (SELECT t.id FROM tsw_operations op JOIN tsw_operation_targets t ON t.operation_id=op.id WHERE op.batch_id=batch.id AND op.operation_type='join')
   OR q.task_type='oauth_generate' AND q.membership_id IN (SELECT m.id FROM tsw_batch_memberships m WHERE m.batch_id=batch.id))) tasks)`

const retryableInvitationSQL = `t.status IN ('failed','blocked','unknown') AND NOT t.platform_request_may_have_reached AND t.invitation_confirmed_at IS NULL AND t.membership_id IS NULL AND COALESCE(t.diagnostic_code,'')<>'seat_type_mismatch' AND NOT EXISTS(SELECT 1 FROM tsw_tasks q WHERE q.operation_target_id=t.id AND q.status IN ('queued','retry_wait','running'))`
