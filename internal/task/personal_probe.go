package task

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/accountsession"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

var ErrPersonalCredentialMissing = errors.New("saved Personal credential missing")

// PersonalProbeProvider must use a saved account-level Personal credential only.
// No identifier, password or TOTP is passed across this boundary. Missing
// saved sessions fail closed without falling back to the legacy target probe.
type PersonalProbeProvider interface {
	ProbePersonal(context.Context, string) (platform.PersonalProbeEvidence, error)
}
type MissingPersonalProvider struct{}

func (MissingPersonalProvider) ProbePersonal(context.Context, string) (platform.PersonalProbeEvidence, error) {
	return platform.PersonalProbeEvidence{}, ErrPersonalCredentialMissing
}

// ProcessPersonalProbes claims one persisted item, obtains an isolated result,
// then conditionally publishes it. A crash or failed publication leaves a running
// item (never normal); stale attempts are fenced by attempt_count on recovery.
func (s *Store) ProcessPersonalProbes(ctx context.Context, provider PersonalProbeProvider) (bool, error) {
	if provider == nil {
		provider = MissingPersonalProvider{}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var batchID, targetID string
	var attempt int
	// Lock batch before its item, the same order as cancellation. Stale running
	// attempts are retried with a new fence; their previous result cannot publish.
	err = tx.QueryRow(ctx, `SELECT batch.id FROM tsw_personal_probe_batches batch WHERE batch.canceled_at IS NULL
 AND (SELECT count(*) FROM tsw_personal_probe_items active WHERE active.batch_id=batch.id AND active.status='running' AND active.started_at>=now()-interval '5 minutes')<batch.task_concurrency
  AND EXISTS(SELECT 1 FROM tsw_personal_probe_items item WHERE item.batch_id=batch.id
   AND (item.status='queued' OR (item.status='running' AND item.started_at<now()-interval '5 minutes')))
  ORDER BY batch.created_at,batch.id FOR UPDATE OF batch SKIP LOCKED LIMIT 1`).Scan(&batchID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var running, limit int
	if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM tsw_personal_probe_items WHERE batch_id=$1 AND status='running' AND started_at>=now()-interval '5 minutes'),task_concurrency FROM tsw_personal_probe_batches WHERE id=$1`, batchID).Scan(&running, &limit); err != nil {
		return false, err
	}
	if running >= limit {
		return false, nil
	}
	err = tx.QueryRow(ctx, `SELECT target_account_id FROM tsw_personal_probe_items WHERE batch_id=$1 AND
  (status='queued' OR (status='running' AND started_at<now()-interval '5 minutes'))
  ORDER BY CASE WHEN status='queued' THEN 0 ELSE 1 END,identifier,target_account_id FOR UPDATE SKIP LOCKED LIMIT 1`, batchID).Scan(&targetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	err = tx.QueryRow(ctx, `UPDATE tsw_personal_probe_items SET status='running',outcome=NULL,endpoint=NULL,http_status=NULL,
  evidence_code=NULL,verified_evidence=false,attempt_count=attempt_count+1,started_at=now(),finished_at=NULL WHERE batch_id=$1 AND target_account_id=$2 RETURNING attempt_count`, batchID, targetID).Scan(&attempt)
	if err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}

	// Never fall back to BasicAuth, the legacy target probe, or a password login.
	evidence, probeErr := provider.ProbePersonal(ctx, targetID)
	outcome := platform.ClassifyPersonalProbe(evidence)
	if errors.Is(probeErr, ErrPersonalCredentialMissing) {
		outcome = platform.PersonalMissingCredential
		evidence = platform.PersonalProbeEvidence{}
	}
	if probeErr != nil && !errors.Is(probeErr, ErrPersonalCredentialMissing) {
		evidence = platform.PersonalProbeEvidence{TransportError: probeErr, SessionGeneration: evidence.SessionGeneration, SessionRevision: evidence.SessionRevision}
		outcome = platform.ClassifyPersonalProbe(evidence)
	}
	status := "failed"
	if outcome == platform.PersonalAvailable {
		status = "succeeded"
	}
	var httpStatus any
	if evidence.HTTPStatus >= 100 && evidence.HTTPStatus <= 599 {
		httpStatus = evidence.HTTPStatus
	}
	var endpoint any
	if httpStatus != nil {
		endpoint = "personal_usage"
	}
	var code any
	if outcome == platform.PersonalMissingCredential {
		code = "personal_credential_absent"
	} else if evidence.ErrorCode != "" && len(evidence.ErrorCode) <= 128 {
		code = evidence.ErrorCode
	}
	verified := (outcome == platform.PersonalBanned && evidence.VerifiedDeactivation && code == "account_deactivated") || (outcome == platform.PersonalAvailable && evidence.VerifiedUsage)
	// A disconnected request must not abort publication; bound the detached write.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	saveTx, err := s.pool.Begin(saveCtx)
	if err != nil {
		return true, err
	}
	defer saveTx.Rollback(saveCtx)
	if evidence.SessionGeneration != "" {
		// Lock in the same order as refresh and credential invalidation: target,
		// credential, access, session. An edit cannot commit between verification
		// and publication; an edit holding a lock wins and makes this attempt stale.
		current, lockErr := lockPersonalGeneration(saveCtx, saveTx, targetID, evidence)
		if lockErr != nil {
			return true, lockErr
		}
		if !current {
			return true, nil
		}
	}
	var lockedBatch string
	err = saveTx.QueryRow(saveCtx, `SELECT id FROM tsw_personal_probe_batches WHERE id=$1 AND canceled_at IS NULL FOR UPDATE`, batchID).Scan(&lockedBatch)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return true, err
	}
	savedItem, err := saveTx.Exec(saveCtx, `UPDATE tsw_personal_probe_items item SET status=$4,outcome=$5,endpoint=$6,http_status=$7,evidence_code=$8,verified_evidence=$9,finished_at=now()
  WHERE item.batch_id=$1 AND item.target_account_id=$2 AND item.attempt_count=$3 AND item.status='running'`, batchID, targetID, attempt, status, string(outcome), endpoint, httpStatus, code, verified)
	if err != nil {
		_ = saveTx.Rollback(saveCtx)
		// Best effort diagnostic only: never convert an unpersisted result to
		// success or claim its classification survived a failed write.
		markerCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer stop()
		_, _ = s.pool.Exec(markerCtx, `UPDATE tsw_personal_probe_items SET evidence_code='result_persistence_failed'
  WHERE batch_id=$1 AND target_account_id=$2 AND attempt_count=$3 AND status='running'`, batchID, targetID, attempt)
		return true, err
	}
	if savedItem.RowsAffected() == 1 && outcome == platform.PersonalCredentialInvalid && evidence.SessionGeneration != "" {
		if err = accountsession.RejectAT(saveCtx, saveTx, targetID, evidence.SessionRevision, evidence.SessionGeneration); err != nil {
			return true, err
		}
	}
	if err = saveTx.Commit(saveCtx); err != nil {
		return true, err
	}
	// Cancellation or retention may remove the item while a provider is running.
	// Batch and item fences prevent that attempt from publishing after cancellation.
	return true, nil
}

func lockPersonalGeneration(ctx context.Context, tx pgx.Tx, targetID string, evidence platform.PersonalProbeEvidence) (bool, error) {
	var active, complete, validExpiry bool
	var revision, attempt, sessionAttempt int64
	var status, generation string
	for _, query := range []struct {
		statement string
		values    []any
	}{
		{`SELECT status='active' FROM tsw_target_accounts WHERE id=$1 FOR UPDATE`, []any{&active}},
		{`SELECT secret_revision,material_status='complete' AND materials_sealed FROM tsw_target_credentials WHERE target_account_id=$1 FOR UPDATE`, []any{&revision, &complete}},
		{`SELECT attempt,status FROM tsw_target_personal_access WHERE target_account_id=$1 FOR UPDATE`, []any{&attempt, &status}},
		{`SELECT attempt,generation::text,expires_at>clock_timestamp() FROM tsw_target_personal_sessions WHERE target_account_id=$1 FOR UPDATE`, []any{&sessionAttempt, &generation, &validExpiry}},
	} {
		err := tx.QueryRow(ctx, query.statement, targetID).Scan(query.values...)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	return active && complete && status == "ready" && validExpiry && revision == evidence.SessionRevision && attempt == sessionAttempt && generation == evidence.SessionGeneration, nil
}
