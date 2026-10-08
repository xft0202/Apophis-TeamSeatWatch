package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

type joinScopeReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Independent batches share a workspace. Only the selected account's existing
// membership or unresolved invitation can conflict with a new authorization.
func joinAccountConflict(ctx context.Context, query joinScopeReader, batch ownerapi.Batch) (string, int, error) {
	var original string
	var count int
	err := query.QueryRow(ctx, `WITH conflicts AS (SELECT previous.id,previous.updated_at,selected.target_account_id
 FROM tsw_batch_targets selected
 JOIN tsw_batches previous ON previous.id<>selected.batch_id
 JOIN tsw_mother_workspace_bindings binding ON binding.id=previous.binding_id AND binding.workspace_id=$2
 WHERE selected.batch_id=$1 AND (
 EXISTS(SELECT 1 FROM tsw_batch_memberships member WHERE member.batch_id=previous.id
  AND member.target_account_id=selected.target_account_id AND member.state='active')
 OR EXISTS(SELECT 1 FROM tsw_operations operation JOIN tsw_operation_targets target ON target.operation_id=operation.id
  WHERE operation.batch_id=previous.id AND operation.operation_type='join'
  AND target.target_account_id=selected.target_account_id
  AND (target.status IN ('queued','running','invited') OR
   target.status IN ('blocked','unknown') AND (target.platform_request_may_have_reached OR target.invitation_confirmed_at IS NOT NULL))))
 ) SELECT id::text,(SELECT count(DISTINCT target_account_id) FROM conflicts) FROM conflicts ORDER BY updated_at DESC,id LIMIT 1`, batch.Id, batch.WorkspaceId).Scan(&original, &count)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, nil
	}
	return original, count, err
}

func joinAccountConflictMessage(count int) string {
	return fmt.Sprintf("所选账号中有 %d 个正在该空间的其他本轮使用，请更换这些账号或查看对应操作", count)
}
