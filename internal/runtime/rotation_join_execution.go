package runtime

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type rotationJoinExecutionFailure string

func (e rotationJoinExecutionFailure) Error() string { return string(e) }

const (
	joinExecutionBusy       rotationJoinExecutionFailure = "join_execution_busy"
	joinExecutionStale      rotationJoinExecutionFailure = "join_execution_stale"
	joinExecutionTransition rotationJoinExecutionFailure = "join_execution_invalid_transition"
)

// This lease only owns journal writes. It is not execution permission: the
// dispatcher independently rechecks current authority before any egress.
type rotationJoinExecutionLease struct {
	slotID, workerID, token uuid.UUID
	epoch                   int64
	expires                 time.Time
	reconcileOnly           bool
	owner                   ownerContext
}

// The owner action gate fences concurrent revocation; DB clocks are still
// checked at each final write. No source writerfence epochs are adopted here.
type rotationJoinBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

func (h *OwnerAuthHandler) joinExecutionTx(ctx context.Context, owner ownerContext) (pgx.Tx, error) {
	return joinExecutionTxOn(ctx, h.pool, owner)
}
func joinExecutionTxOn(ctx context.Context, db rotationJoinBeginner, owner ownerContext) (pgx.Tx, error) {
	oid, err := uuid.Parse(owner.OwnerID)
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended('tsw.rotation.action.owner/'||$1::text,0))`, oid.String()); err == nil {
		err = rotationJoinOwnerLegal(ctx, tx, owner)
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func (h *OwnerAuthHandler) claimRotationJoinExecution(ctx context.Context, owner ownerContext, previewID, slotID, workerID uuid.UUID, leaseDuration time.Duration) (rotationJoinExecutionLease, error) {
	l := rotationJoinExecutionLease{slotID: slotID, workerID: workerID, token: uuid.New(), owner: owner}
	if workerID == uuid.Nil || leaseDuration.Microseconds() <= 0 {
		return l, joinExecutionTransition
	}
	tx, err := h.joinExecutionTx(ctx, owner)
	if err != nil {
		return l, err
	}
	defer tx.Rollback(ctx)
	// INSERT conflict arbitration followed by a row lock serializes even the very
	// first claim, without a second pool connection or an unlocked existence test.
	_, err = tx.Exec(ctx, `INSERT INTO public.tsw_rotation_join_executions(slot_id,preview_id,workspace_id,owner_id,candidate_account_id,original_platform_member_id,candidate_identifier,seat_type,authorization_digest,authorized_session,epoch_versions,created_at)
 SELECT slot_id,preview_id,workspace_id,owner_id,candidate_account_id,original_platform_member_id,candidate_identifier,seat_type,authorization_digest,authorized_session,epoch_versions,created_at FROM public.tsw_rotation_candidate_join_intents WHERE slot_id=$1 AND preview_id=$2 AND owner_id=$3 ON CONFLICT(slot_id) DO NOTHING`, slotID, previewID, owner.OwnerID)
	if err != nil {
		return l, err
	}
	var state string
	var busy, marked bool
	err = tx.QueryRow(ctx, `SELECT state,COALESCE(lease_expires_at>clock_timestamp(),false),request_may_have_reached OR accept_may_have_reached FROM public.tsw_rotation_join_executions WHERE slot_id=$1 AND preview_id=$2 AND owner_id=$3 FOR UPDATE`, slotID, previewID, owner.OwnerID).Scan(&state, &busy, &marked)
	if err != nil {
		return l, err
	}
	if busy {
		return l, joinExecutionBusy
	}
	l.reconcileOnly = marked || state == "reconcile_required"
	if state == "blocked" && !l.reconcileOnly {
		return l, joinExecutionTransition
	}
	err = tx.QueryRow(ctx, `UPDATE public.tsw_rotation_join_executions e SET state=CASE WHEN $4 THEN 'reconcile_required' ELSE 'ready' END,lease_owner=$2,lease_token=$3,lease_epoch=lease_epoch+1,
 lease_expires_at=LEAST(clock_timestamp()+$5::bigint*interval '1 microsecond',(SELECT LEAST(idle_expires_at,absolute_expires_at) FROM public.tsw_owner_sessions WHERE id=$6)),last_stage=CASE WHEN $4 THEN 'reconcile' ELSE last_stage END,last_outcome=CASE WHEN $4 THEN 'started' ELSE last_outcome END,updated_at=clock_timestamp()
 WHERE slot_id=$1 AND (lease_expires_at IS NULL OR lease_expires_at<=clock_timestamp()) AND EXISTS(SELECT 1 FROM public.tsw_owner_sessions s JOIN public.tsw_owners o ON o.id=s.owner_id WHERE s.id=$6 AND s.owner_id=$7 AND s.auth_version=o.auth_version AND s.revoked_at IS NULL AND s.idle_expires_at>clock_timestamp() AND s.absolute_expires_at>clock_timestamp()) RETURNING lease_epoch,lease_expires_at`, slotID, workerID, l.token, l.reconcileOnly, leaseDuration.Microseconds(), owner.SessionID, owner.OwnerID).Scan(&l.epoch, &l.expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, joinExecutionStale
	}
	if err != nil {
		return l, err
	}
	if l.reconcileOnly {
		_, err = tx.Exec(ctx, `INSERT INTO public.tsw_rotation_join_execution_attempts(slot_id,lease_epoch,stage,attempt_no,event_kind,may_have_reached,outcome,started_at)
 SELECT $1,$2,'reconcile',COALESCE(max(attempt_no),0)+1,'started',false,'started',clock_timestamp() FROM public.tsw_rotation_join_execution_attempts WHERE slot_id=$1 AND stage='reconcile'`, slotID, l.epoch)
		if err != nil {
			return l, err
		}
	}
	err = tx.Commit(ctx)
	return l, err
}

const joinExecutionCurrentSession = `EXISTS(SELECT 1 FROM public.tsw_owner_sessions s JOIN public.tsw_owners o ON o.id=s.owner_id WHERE s.id=$5 AND s.owner_id=$6 AND s.auth_version=o.auth_version AND s.revoked_at IS NULL AND s.idle_expires_at>clock_timestamp() AND s.absolute_expires_at>clock_timestamp())`
const joinExecutionCAS = `slot_id=$1 AND lease_owner=$2 AND lease_token=$3 AND lease_epoch=$4 AND lease_expires_at>clock_timestamp() AND owner_id=$6 AND ` + joinExecutionCurrentSession

func (h *OwnerAuthHandler) lockedJoinExecution(ctx context.Context, l rotationJoinExecutionLease) (pgx.Tx, string, error) {
	return lockedJoinExecutionOn(ctx, h.pool, l)
}
func lockedJoinExecutionOn(ctx context.Context, db rotationJoinBeginner, l rotationJoinExecutionLease) (pgx.Tx, string, error) {
	tx, err := joinExecutionTxOn(ctx, db, l.owner)
	if err != nil {
		return nil, "", err
	}
	var state string
	err = tx.QueryRow(ctx, `SELECT state FROM public.tsw_rotation_join_executions WHERE `+joinExecutionCAS+` FOR UPDATE`, l.slotID, l.workerID, l.token, l.epoch, l.owner.SessionID, l.owner.OwnerID).Scan(&state)
	if err != nil {
		_ = tx.Rollback(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			err = joinExecutionStale
		}
		return nil, "", err
	}
	return tx, state, nil
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

// The dispatcher adds admitted clocks/bindings to this same final CAS write.
func markRotationJoinStageInTx(ctx context.Context, tx pgx.Tx, l rotationJoinExecutionLease, state, stage, guard string, extra []any) error {
	if l.reconcileOnly || stage == "request_join" && state != "ready" || stage == "accept_join" && state != "request_succeeded" {
		return joinExecutionTransition
	}
	column, next := "request", "request_started"
	if stage == "accept_join" {
		column, next = "accept", "accept_started"
	}
	// Conservatively mark may-have-reached BEFORE a dispatcher can send.
	// Neither a later failure nor recovery may clear this durable uncertainty.
	args := append([]any{l.slotID, l.workerID, l.token, l.epoch, l.owner.SessionID, l.owner.OwnerID, next}, extra...)
	tag, err := tx.Exec(ctx, `UPDATE public.tsw_rotation_join_executions SET state=$7,`+column+`_attempt_count=`+column+`_attempt_count+1,`+column+`_may_have_reached=true,last_stage='`+stage+`',last_outcome='started',updated_at=clock_timestamp() WHERE `+joinExecutionCAS+guard, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return joinExecutionStale
	}
	_, err = tx.Exec(ctx, `INSERT INTO public.tsw_rotation_join_execution_attempts(slot_id,lease_epoch,stage,attempt_no,event_kind,may_have_reached,outcome,started_at) SELECT slot_id,lease_epoch,$2,`+column+`_attempt_count,'started',true,'started',clock_timestamp() FROM public.tsw_rotation_join_executions WHERE slot_id=$1`, l.slotID, stage)
	if err != nil {
		return err
	}
	return nil
}

func (h *OwnerAuthHandler) finishRotationJoinStage(ctx context.Context, l rotationJoinExecutionLease, stage, outcome string, mayHaveReached bool) error {
	return finishRotationJoinStageOn(ctx, h.pool, l, stage, outcome, mayHaveReached)
}
func finishRotationJoinStageOn(ctx context.Context, db rotationJoinBeginner, l rotationJoinExecutionLease, stage, outcome string, mayHaveReached bool) error {
	if (stage != "request_join" && stage != "accept_join" && stage != "reconcile") || (outcome != "succeeded" && outcome != "failed" && outcome != "uncertain") || stage == "reconcile" && mayHaveReached {
		return joinExecutionTransition
	}
	tx, state, err := lockedJoinExecutionOn(ctx, db, l)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	expected := map[string]string{"request_join": "request_started", "accept_join": "accept_started", "reconcile": "reconcile_required"}[stage]
	if state != expected {
		return joinExecutionTransition
	}
	var start uuid.UUID
	var attempt int
	var started time.Time
	err = tx.QueryRow(ctx, `SELECT a.id,a.attempt_no,a.started_at FROM public.tsw_rotation_join_execution_attempts a WHERE a.slot_id=$1 AND a.lease_epoch=$2 AND a.stage=$3 AND a.event_kind='started' AND NOT EXISTS(SELECT 1 FROM public.tsw_rotation_join_execution_attempts f WHERE f.start_event_id=a.id)`, l.slotID, l.epoch, stage).Scan(&start, &attempt, &started)
	if errors.Is(err, pgx.ErrNoRows) {
		return joinExecutionTransition
	}
	if err != nil {
		return err
	}
	// Terminal events append; the original start (including uncertainty) never changes.
	_, err = tx.Exec(ctx, `INSERT INTO public.tsw_rotation_join_execution_attempts(slot_id,lease_epoch,stage,attempt_no,event_kind,start_event_id,may_have_reached,outcome,started_at,finished_at) VALUES($1,$2,$3,$4,'finished',$5,$6,$7,$8,clock_timestamp())`, l.slotID, l.epoch, stage, attempt, start, mayHaveReached, outcome, started)
	if err != nil {
		return err
	}
	next, release := "reconcile_required", true
	if stage == "request_join" && outcome == "succeeded" {
		next, release = "request_succeeded", false
	}
	tag, err := tx.Exec(ctx, `UPDATE public.tsw_rotation_join_executions SET state=$7,last_stage=$8,last_outcome=$9,lease_owner=CASE WHEN $10 THEN NULL ELSE lease_owner END,lease_token=CASE WHEN $10 THEN NULL ELSE lease_token END,lease_expires_at=CASE WHEN $10 THEN NULL ELSE lease_expires_at END,updated_at=clock_timestamp() WHERE `+joinExecutionCAS, l.slotID, l.workerID, l.token, l.epoch, l.owner.SessionID, l.owner.OwnerID, next, stage, outcome, release)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return joinExecutionStale
	}
	return tx.Commit(ctx)
}
