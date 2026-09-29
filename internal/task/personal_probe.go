package task

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

var ErrPersonalCredentialMissing = errors.New("saved Personal credential missing")

// PersonalProbeProvider must use a saved account-level Personal credential only.
// No identifier, password or TOTP is passed across this boundary. Production
// fails closed until an independently reviewed Personal credential path exists.
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
  AND EXISTS(SELECT 1 FROM tsw_personal_probe_items item WHERE item.batch_id=batch.id
   AND (item.status='queued' OR (item.status='running' AND item.started_at<now()-interval '5 minutes')))
  ORDER BY batch.created_at,batch.id FOR UPDATE OF batch SKIP LOCKED LIMIT 1`).Scan(&batchID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
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
		evidence = platform.PersonalProbeEvidence{TransportError: probeErr}
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
	result, err := s.pool.Exec(saveCtx, `UPDATE tsw_personal_probe_items item SET status=$4,outcome=$5,endpoint=$6,http_status=$7,evidence_code=$8,verified_evidence=$9,finished_at=now()
  FROM tsw_personal_probe_batches batch WHERE item.batch_id=$1 AND item.target_account_id=$2 AND item.attempt_count=$3
  AND item.status='running' AND batch.id=item.batch_id AND batch.canceled_at IS NULL`, batchID, targetID, attempt, status, string(outcome), endpoint, httpStatus, code, verified)
	if err != nil {
		// Best effort diagnostic only: never convert an unpersisted result to
		// success or claim its classification survived a failed write.
		markerCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer stop()
		_, _ = s.pool.Exec(markerCtx, `UPDATE tsw_personal_probe_items SET evidence_code='result_persistence_failed'
  WHERE batch_id=$1 AND target_account_id=$2 AND attempt_count=$3 AND status='running'`, batchID, targetID, attempt)
		return true, err
	}
	// Cancellation or retention may remove the item while a provider is running.
	// Fencing makes that result impossible to publish or recreate.
	_ = result
	return true, nil
}
