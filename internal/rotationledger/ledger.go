// Package rotationledger owns the durable account×Workspace usage evidence and
// account-global customer-delivery protection consumed by Ticket10.
package rotationledger

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ Pool *pgxpool.Pool }

type UsageEvidence struct {
	TargetAccountID uuid.UUID
	WorkspaceID     uuid.UUID
	State           string
	Source          string
	EvidenceID      string
	ObservedAt      time.Time
	ExpiresAt       time.Time
}

type Usage struct {
	UsageEvidence
	EverUsed bool
	Version  int64
}

type ProtectionEvidence struct {
	TargetAccountID uuid.UUID
	Status          string
	Source          string
	EvidenceID      string
	ObservedAt      time.Time
}

type Protection struct {
	ProtectionEvidence
	Version int64
}

func validEvidence(source, id string, observed time.Time) bool {
	return strings.TrimSpace(source) != "" && len(source) <= 128 && len(id) == 64 && !observed.IsZero() && !observed.After(time.Now().Add(time.Minute))
}

func (s Store) RecordUsage(ctx context.Context, in UsageEvidence) (*Usage, error) {
	if s.Pool == nil || in.TargetAccountID == uuid.Nil || in.WorkspaceID == uuid.Nil ||
		(in.State != "unknown" && in.State != "never_used" && in.State != "used") ||
		!validEvidence(in.Source, in.EvidenceID, in.ObservedAt) || !in.ExpiresAt.After(in.ObservedAt) || in.ExpiresAt.After(in.ObservedAt.Add(5*time.Minute)) {
		return nil, errors.New("invalid rotation usage evidence")
	}
	var out Usage
	err := s.Pool.QueryRow(ctx, `INSERT INTO public.tsw_rotation_usage_ledger
		(target_account_id,workspace_id,usage_state,ever_used,evidence_source,evidence_id,observed_at,expires_at)
		VALUES($1,$2,$3,$3='used',$4,$5,$6,$7)
		ON CONFLICT(target_account_id,workspace_id) DO UPDATE SET
		usage_state=CASE WHEN tsw_rotation_usage_ledger.usage_state='used' OR EXCLUDED.usage_state='used' THEN 'used'
		 WHEN tsw_rotation_usage_ledger.usage_state='never_used' OR EXCLUDED.usage_state='never_used' THEN 'never_used' ELSE 'unknown' END,
		ever_used=tsw_rotation_usage_ledger.ever_used OR EXCLUDED.ever_used,
		evidence_source=CASE WHEN EXCLUDED.usage_state<>'unknown' THEN EXCLUDED.evidence_source ELSE tsw_rotation_usage_ledger.evidence_source END,
		evidence_id=CASE WHEN EXCLUDED.usage_state<>'unknown' THEN EXCLUDED.evidence_id ELSE tsw_rotation_usage_ledger.evidence_id END,
		observed_at=CASE WHEN EXCLUDED.usage_state<>'unknown' THEN EXCLUDED.observed_at ELSE tsw_rotation_usage_ledger.observed_at END,
		expires_at=CASE WHEN EXCLUDED.usage_state<>'unknown' THEN EXCLUDED.expires_at ELSE tsw_rotation_usage_ledger.expires_at END,
		version=tsw_rotation_usage_ledger.version+1
		RETURNING target_account_id,workspace_id,usage_state,ever_used,evidence_source,evidence_id,observed_at,expires_at,version`,
		in.TargetAccountID, in.WorkspaceID, in.State, strings.TrimSpace(in.Source), in.EvidenceID, in.ObservedAt.UTC(), in.ExpiresAt.UTC()).Scan(
		&out.TargetAccountID, &out.WorkspaceID, &out.State, &out.EverUsed, &out.Source, &out.EvidenceID, &out.ObservedAt, &out.ExpiresAt, &out.Version)
	if err != nil {
		return nil, fmt.Errorf("record rotation usage evidence: %w", err)
	}
	return &out, nil
}

func (s Store) GetUsage(ctx context.Context, accountID, workspaceID uuid.UUID) (*Usage, error) {
	if s.Pool == nil || accountID == uuid.Nil || workspaceID == uuid.Nil {
		return nil, errors.New("invalid rotation usage lookup")
	}
	var out Usage
	err := s.Pool.QueryRow(ctx, `SELECT target_account_id,workspace_id,usage_state,ever_used,evidence_source,evidence_id,observed_at,expires_at,version
		FROM public.tsw_rotation_usage_ledger WHERE target_account_id=$1 AND workspace_id=$2`, accountID, workspaceID).Scan(
		&out.TargetAccountID, &out.WorkspaceID, &out.State, &out.EverUsed, &out.Source, &out.EvidenceID, &out.ObservedAt, &out.ExpiresAt, &out.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read rotation usage evidence: %w", err)
	}
	return &out, nil
}

func (s Store) SetProtection(ctx context.Context, in ProtectionEvidence) (*Protection, error) {
	if s.Pool == nil || in.TargetAccountID == uuid.Nil || !validEvidence(in.Source, in.EvidenceID, in.ObservedAt) ||
		(in.Status != "none" && in.Status != "suspected_sold" && in.Status != "sale_reserved" && in.Status != "delivery_pending" && in.Status != "delivered" && in.Status != "canceled_retired") {
		return nil, errors.New("invalid rotation protection evidence")
	}
	var out Protection
	err := s.Pool.QueryRow(ctx, `INSERT INTO public.tsw_rotation_global_protections
		(target_account_id,status,evidence_source,evidence_id,observed_at) VALUES($1,$2,$3,$4,$5)
		ON CONFLICT(target_account_id) DO UPDATE SET status=EXCLUDED.status,evidence_source=EXCLUDED.evidence_source,
		evidence_id=EXCLUDED.evidence_id,observed_at=EXCLUDED.observed_at,version=tsw_rotation_global_protections.version+1
		RETURNING target_account_id,status,evidence_source,evidence_id,observed_at,version`,
		in.TargetAccountID, in.Status, strings.TrimSpace(in.Source), in.EvidenceID, in.ObservedAt.UTC()).Scan(
		&out.TargetAccountID, &out.Status, &out.Source, &out.EvidenceID, &out.ObservedAt, &out.Version)
	if err != nil {
		return nil, fmt.Errorf("record rotation protection evidence: %w", err)
	}
	return &out, nil
}

func (s Store) GetProtection(ctx context.Context, accountID uuid.UUID) (*Protection, error) {
	if s.Pool == nil || accountID == uuid.Nil {
		return nil, errors.New("invalid rotation protection lookup")
	}
	var out Protection
	err := s.Pool.QueryRow(ctx, `SELECT target_account_id,status,evidence_source,evidence_id,observed_at,version
		FROM public.tsw_rotation_global_protections WHERE target_account_id=$1`, accountID).Scan(
		&out.TargetAccountID, &out.Status, &out.Source, &out.EvidenceID, &out.ObservedAt, &out.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read rotation protection evidence: %w", err)
	}
	return &out, nil
}
