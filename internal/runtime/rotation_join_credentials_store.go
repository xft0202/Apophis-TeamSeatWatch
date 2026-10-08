package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

const (
	joinCredentialsBusy   rotationJoinExecutionFailure = "join_credentials_busy"
	joinCredentialsStale  rotationJoinExecutionFailure = "join_credentials_stale"
	joinCredentialsReview rotationJoinExecutionFailure = "join_credentials_review_required"
	joinCredentialsRepair rotationJoinExecutionFailure = "join_credentials_repair_required"
)

type joinCredentialAttempt struct {
	binding                           joinCredentialBinding
	subject, member, membershipDigest string
	membershipAttempt                 int
	lease                             rotationJoinExecutionLease
}

func readJoinCredentialAttempt(ctx context.Context, db rotationRow, slot uuid.UUID) (joinCredentialAttempt, error) {
	a := joinCredentialAttempt{}
	a.binding.slot = slot
	err := db.QueryRow(ctx, `SELECT attempt_id,generation,candidate_account_id,workspace_id,platform_workspace_id,secret_revision,subject_id,platform_member_id,membership_attempt,membership_digest FROM public.tsw_rotation_join_credential_attempts WHERE slot_id=$1`, slot).Scan(&a.binding.attempt, &a.binding.generation, &a.binding.target, &a.binding.workspace, &a.binding.platformWorkspace, &a.binding.revision, &a.subject, &a.member, &a.membershipAttempt, &a.membershipDigest)
	return a, err
}
func (h *OwnerAuthHandler) claimJoinCredentialAttempt(ctx context.Context, tx pgx.Tx, i rotationJoinIntent, p joinAdmission, owner ownerContext, worker uuid.UUID, duration time.Duration, repair bool) (joinCredentialAttempt, error) {
	a, err := readJoinCredentialAttempt(ctx, tx, i.slotID)
	if errors.Is(err, pgx.ErrNoRows) {
		b := p.binding
		// Latest evidence, not an earlier successful observation hidden by unknown.
		_, err = tx.Exec(ctx, `INSERT INTO public.tsw_rotation_join_credential_attempts(slot_id,attempt_id,generation,candidate_account_id,workspace_id,platform_workspace_id,secret_revision,subject_id,membership_attempt,membership_digest,platform_member_id)
 SELECT $1,$2,$3,$4,$5,$6,$7,bind.subject_id,ev.reconciliation_attempt,ev.evidence_digest,ev.platform_member_id FROM public.tsw_rotation_join_personal_bindings bind JOIN public.tsw_rotation_join_membership_evidence ev USING(slot_id) WHERE bind.slot_id=$1 AND ev.result='confirmed' AND ev.observed_at>clock_timestamp()-interval '30 seconds' AND ev.reconciliation_attempt=(SELECT max(reconciliation_attempt) FROM public.tsw_rotation_join_membership_evidence WHERE slot_id=$1)`, i.slotID, uuid.New(), uuid.New(), i.candidateAccountID, i.workspaceID, p.platformID, b.Revision)
		if err != nil {
			return a, err
		}
		a, err = readJoinCredentialAttempt(ctx, tx, i.slotID)
	} else if err == nil && !repair {
		return a, joinCredentialsRepair
	}
	if err != nil {
		return a, err
	}
	if a.binding.target != i.candidateAccountID || a.binding.workspace != i.workspaceID || a.binding.platformWorkspace != p.platformID || a.binding.revision != p.binding.Revision || requireJoinPersonalBinding(ctx, tx, i, p, a.subject) != nil {
		return a, joinCredentialsStale
	}
	var busy bool
	if err = tx.QueryRow(ctx, `SELECT COALESCE(lease_expires_at>clock_timestamp(),false) FROM public.tsw_rotation_join_credential_attempts WHERE slot_id=$1 FOR UPDATE`, i.slotID).Scan(&busy); err != nil {
		return a, err
	}
	if busy {
		return a, joinCredentialsBusy
	}
	l := rotationJoinExecutionLease{slotID: i.slotID, workerID: worker, token: uuid.New(), owner: owner}
	err = tx.QueryRow(ctx, `UPDATE public.tsw_rotation_join_credential_attempts SET lease_owner=$2,lease_token=$3,lease_epoch=lease_epoch+1,owner_session=$4,lease_expires_at=LEAST(clock_timestamp()+$5::bigint*interval '1 microsecond',(SELECT LEAST(idle_expires_at,absolute_expires_at) FROM public.tsw_owner_sessions WHERE id=$4)) WHERE slot_id=$1 AND (lease_expires_at IS NULL OR lease_expires_at<=clock_timestamp()) RETURNING lease_epoch,lease_expires_at`, i.slotID, worker, l.token, owner.SessionID, duration.Microseconds()).Scan(&l.epoch, &l.expires)
	a.lease = l
	return a, err
}

const credentialCAS = `slot_id=$1 AND lease_owner=$2 AND lease_token=$3 AND lease_epoch=$4 AND owner_session=$5 AND lease_expires_at>clock_timestamp() AND public.tsw_rotation_join_credential_authority(slot_id,$5)`

func credentialLeaseArgs(a joinCredentialAttempt) []any {
	l := a.lease
	return []any{l.slotID, l.workerID, l.token, l.epoch, l.owner.SessionID}
}
func requireCredentialLease(ctx context.Context, db rotationRow, a joinCredentialAttempt) error {
	var current bool
	err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.tsw_rotation_join_credential_attempts WHERE `+credentialCAS+`)`, credentialLeaseArgs(a)...).Scan(&current)
	if err != nil {
		return err
	}
	if !current {
		return joinCredentialsStale
	}
	return nil
}
func appendCredentialEvent(ctx context.Context, tx pgx.Tx, a joinCredentialAttempt, stage, event string, observed time.Time) error {
	l := a.lease
	_, err := tx.Exec(ctx, `INSERT INTO public.tsw_rotation_join_credential_events(attempt_id,stage,event,lease_epoch,lease_owner,lease_token,owner_session,observed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, a.binding.attempt, stage, event, l.epoch, l.workerID, l.token, l.owner.SessionID, observed)
	return err
}
func (h *OwnerAuthHandler) appendCredentialComponent(ctx context.Context, tx pgx.Tx, a joinCredentialAttempt, kind string, value any, observed, expires time.Time) error {
	b := a.binding
	b.kind = kind
	v, n, c, err := sealJoinCredential(h.keyRing, b, value)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(c)
	l := a.lease
	_, err = tx.Exec(ctx, `INSERT INTO public.tsw_rotation_join_credential_components(attempt_id,kind,key_version,nonce,sealed,sealed_digest,observed_at,expires_at,lease_epoch,lease_owner,lease_token,owner_session) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, b.attempt, kind, int16(v), n, c, hex.EncodeToString(digest[:]), observed, expires.UTC().Truncate(time.Microsecond), l.epoch, l.workerID, l.token, l.owner.SessionID)
	return err
}

type joinCredentialComponent struct {
	digest            string
	observed, expires time.Time
	key               int16
	nonce, sealed     []byte
}

func (h *OwnerAuthHandler) readCredentialComponent(ctx context.Context, db rotationRow, a joinCredentialAttempt, kind string, out any) (joinCredentialComponent, error) {
	c := joinCredentialComponent{}
	err := db.QueryRow(ctx, `SELECT sealed_digest,observed_at,expires_at,key_version,nonce,sealed FROM public.tsw_rotation_join_credential_components WHERE attempt_id=$1 AND kind=$2`, a.binding.attempt, kind).Scan(&c.digest, &c.observed, &c.expires, &c.key, &c.nonce, &c.sealed)
	if err != nil {
		return c, err
	}
	digest := sha256.Sum256(c.sealed)
	if hex.EncodeToString(digest[:]) != c.digest {
		return c, joinCredentialsReview
	}
	b := a.binding
	b.kind = kind
	err = openJoinCredential(h.keyRing, b, uint16(c.key), c.nonce, c.sealed, out)
	if err != nil {
		return c, joinCredentialsReview
	}
	return c, nil
}
func (h *OwnerAuthHandler) validateSavedJoinCredentials(ctx context.Context, db rotationRow, a joinCredentialAttempt) (joinCredentialComponent, joinCredentialComponent, error) {
	var web platform.WorkspaceAccess
	w, err := h.readCredentialComponent(ctx, db, a, "web", &web)
	if err != nil {
		return w, joinCredentialComponent{}, err
	}
	now := time.Now().UTC()
	if !platform.ValidateRotationCandidateWorkspace(web, a.binding.platformWorkspace, a.subject, now) || !web.ExpiresAt.Equal(w.expires) {
		return w, joinCredentialComponent{}, joinCredentialsReview
	}
	var oauth platform.DeliveryCredentialSet
	o, err := h.readCredentialComponent(ctx, db, a, "oauth", &oauth)
	if err != nil {
		return w, o, err
	}
	expiry, err := platform.ValidateRotationCandidateOAuth(oauth, a.binding.platformWorkspace, a.subject, o.observed)
	// ExpiresIn is measured at the original response, not reset by repair.
	if err != nil || !expiry.UTC().Truncate(time.Microsecond).Equal(o.expires) || !o.expires.After(now.Add(time.Minute)) {
		return w, o, joinCredentialsReview
	}
	return w, o, nil
}
func publishJoinCredentials(ctx context.Context, tx pgx.Tx, a joinCredentialAttempt, w, o joinCredentialComponent, verified time.Time) error {
	l := a.lease
	_, err := tx.Exec(ctx, `INSERT INTO public.tsw_rotation_join_credential_generations(attempt_id,generation,candidate_account_id,workspace_id,web_digest,oauth_digest,membership_digest,verified_at,expires_at,lease_epoch,lease_owner,lease_token,owner_session) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, a.binding.attempt, a.binding.generation, a.binding.target, a.binding.workspace, w.digest, o.digest, a.membershipDigest, verified, minTime(w.expires, o.expires), l.epoch, l.workerID, l.token, l.owner.SessionID)
	return err
}
