package runtime

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/task"
)

var errOperationAuthority = errors.New("operation workspace authority changed")

type operationJoinAdapter struct {
	owner             *OwnerAuthHandler
	client            *http.Client
	target            task.JoinTarget
	mother, workspace uuid.UUID
	exchange          uuid.UUID
}

func (h *OwnerAuthHandler) operationJoiner(ctx context.Context, client *http.Client, target task.JoinTarget) (platform.Joiner, error) {
	mother, err := uuid.Parse(target.MotherID)
	if err != nil {
		return nil, errOperationAuthority
	}
	workspace, err := uuid.Parse(target.WorkspaceID)
	if err != nil || target.SeatType != "prolite" {
		return nil, errOperationAuthority
	}
	adapter := &operationJoinAdapter{owner: h, client: client, target: target, mother: mother, workspace: workspace}
	var snapshotWorkspace, snapshotMother uuid.UUID
	err = h.pool.QueryRow(ctx, `SELECT workspace_id,mother_account_id,token_exchange_id FROM tsw_workspace_verifications WHERE id=$1`, target.VerificationID).Scan(&snapshotWorkspace, &snapshotMother, &adapter.exchange)
	if err != nil || snapshotWorkspace != workspace || snapshotMother != mother {
		return nil, errOperationAuthority
	}
	if _, err = adapter.authority(ctx); err != nil {
		return nil, err
	}
	return adapter, nil
}

func (a *operationJoinAdapter) authority(ctx context.Context) (platform.WorkspaceAccess, error) {
	tx, err := a.owner.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return platform.WorkspaceAccess{}, errOperationAuthority
	}
	defer tx.Rollback(ctx)
	facts, _, access, err := a.owner.selectedWorkspaceSnapshotTx(ctx, tx, a.workspace, a.mother)
	if err != nil || facts.Status != "verified" || !facts.CanManage || facts.ExchangeId == nil || *facts.ExchangeId != a.exchange || access.WorkspaceID != a.target.PlatformWorkspace {
		return platform.WorkspaceAccess{}, errOperationAuthority
	}
	if err = tx.Commit(ctx); err != nil {
		return platform.WorkspaceAccess{}, errOperationAuthority
	}
	return access, nil
}

func (a *operationJoinAdapter) Invite(ctx context.Context, workspace, identifier string) (platform.JoinAttemptResult, error) {
	if workspace != a.target.PlatformWorkspace || identifier != a.target.TargetIdentifier {
		return platform.JoinAttemptResult{}, errOperationAuthority
	}
	access, err := a.authority(ctx)
	if err != nil {
		return platform.JoinAttemptResult{}, err
	}
	tx, err := a.owner.pool.Begin(ctx)
	if err != nil {
		return platform.JoinAttemptResult{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockInvitationCapacity(ctx, tx, a.workspace); err != nil {
		return platform.JoinAttemptResult{}, err
	}

	return (platform.WorkspaceInvitation{Client: a.client, Access: access, Workspace: workspace}).Send(ctx, identifier)
}

func (a *operationJoinAdapter) VerifyMembership(ctx context.Context, workspace, identifier, subject string) (platform.MembershipResult, error) {
	if workspace != a.target.PlatformWorkspace || identifier != a.target.TargetIdentifier {
		return platform.MembershipResult{}, errOperationAuthority
	}
	access, err := a.authority(ctx)
	if err != nil {
		return platform.MembershipResult{}, err
	}
	return (platform.WorkspaceInvitation{Client: a.client, Access: access, Workspace: workspace, RecipientSubject: subject}).Membership(ctx, identifier)
}
