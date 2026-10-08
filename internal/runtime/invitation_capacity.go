package runtime

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

// Admission and sending serialize on the same workspace, so different batches
// cannot both reserve the final seat from the same saved observation.
func lockInvitationCapacity(ctx context.Context, tx pgx.Tx, workspaceID uuid.UUID) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('premium-invitations:' || $1::text,0))`, workspaceID.String())
	return err
}

func invitationCapacityTx(ctx context.Context, tx pgx.Tx, workspaceID uuid.UUID, verificationID int64, facts ownerapi.SelectedWorkspaceVerification, targets []uuid.UUID) (platform.InvitationCapacity, []uuid.UUID, error) {
	var reserved int
	var newInvitations []uuid.UUID
	err := tx.QueryRow(ctx, `WITH reservations AS (
 SELECT identifier FROM tsw_workspace_verification_entries WHERE verification_id=$2 AND kind='pending_invite' AND seat_type='prolite'
 UNION
 SELECT account.identifier FROM tsw_operations operation
 JOIN tsw_operation_targets target ON target.operation_id=operation.id
 LEFT JOIN tsw_batch_memberships membership ON membership.id=target.membership_id
 JOIN tsw_target_accounts account ON account.id=COALESCE(target.target_account_id,membership.target_account_id)
 JOIN tsw_batches batch ON batch.id=operation.batch_id
 WHERE operation.workspace_id=$1 AND operation.operation_type='join' AND batch.status<>'ended'
 AND (membership.id IS NULL OR membership.state='active')
 AND (membership.state='active' OR target.invitation_confirmed_at > (SELECT observed_at FROM tsw_workspace_verifications WHERE id=$2)
  OR target.platform_request_may_have_reached AND target.status IN ('unknown','blocked','running')
  OR EXISTS(SELECT 1 FROM tsw_tasks task WHERE task.operation_target_id=target.id AND task.task_type='join' AND task.status IN ('queued','running','retry_wait')))
), outstanding AS (
 SELECT identifier FROM reservations reserved WHERE NOT EXISTS(SELECT 1 FROM tsw_workspace_verification_entries member WHERE member.verification_id=$2 AND member.kind='member' AND member.identifier=reserved.identifier)
) SELECT (SELECT count(*) FROM outstanding),
 ARRAY(SELECT account.id FROM tsw_target_accounts account WHERE account.id=ANY($3::uuid[])
  AND NOT EXISTS(SELECT 1 FROM tsw_workspace_verification_entries entry WHERE entry.verification_id=$2 AND entry.identifier=account.identifier AND entry.seat_type='prolite')
  AND NOT EXISTS(SELECT 1 FROM reservations reserved WHERE reserved.identifier=account.identifier))`, workspaceID, verificationID, targets).Scan(&reserved, &newInvitations)
	if err != nil {
		return platform.InvitationCapacity{}, nil, err
	}
	var entitlements, occupants map[string]int
	if facts.SeatEntitlements != nil {
		entitlements = *facts.SeatEntitlements
	}
	if facts.SeatTypeCounts != nil {
		occupants = *facts.SeatTypeCounts
	}
	return platform.PremiumInvitationCapacity(entitlements, occupants, reserved), newInvitations, nil
}

func invitationCapacityBlocker(capacity platform.InvitationCapacity, requested int) *ownerapi.PreviewBlocker {
	if requested == 0 {
		return nil
	}
	if !capacity.Known {
		return &ownerapi.PreviewBlocker{Code: "premium_capacity_unknown", Message: "未获取高级席位开通数，请在空间管理同步后再邀请"}
	}
	if capacity.Remaining == 0 {
		return &ownerapi.PreviewBlocker{Code: "premium_capacity_exceeded", Message: fmt.Sprintf("高级席位已开通 %d 个、已占用 %d 个、待接受或处理中 %d 个，剩余可邀请 %d 个；本次 %d 个账号等待席位，已有已确认账号可继续处理", capacity.Opened, capacity.Occupied, capacity.Reserved, capacity.Remaining, requested)}
	}
	return nil
}
