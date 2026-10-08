//go:build integration

package runtime

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// Test fixtures exercise the durable CAS without dispatching a platform request.
// Production reaches these writes through the admitted dispatcher transaction.
func (h *OwnerAuthHandler) lockedJoinExecution(ctx context.Context, l rotationJoinExecutionLease) (pgx.Tx, string, error) {
	return lockedJoinExecutionOn(ctx, h.pool, l)
}
func (h *OwnerAuthHandler) markRotationJoinStageStarted(ctx context.Context, l rotationJoinExecutionLease, stage string) error {
	if stage != "request_join" && stage != "accept_join" {
		return joinExecutionTransition
	}
	tx, state, err := h.lockedJoinExecution(ctx, l)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = markRotationJoinStageInTx(ctx, tx, l, state, stage, "", nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (h *OwnerAuthHandler) finishRotationJoinStage(ctx context.Context, l rotationJoinExecutionLease, stage, outcome string, mayHaveReached bool) error {
	return finishRotationJoinStageOn(ctx, h.pool, l, stage, outcome, mayHaveReached)
}
