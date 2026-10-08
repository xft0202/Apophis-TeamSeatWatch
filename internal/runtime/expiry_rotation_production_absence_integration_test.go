//go:build integration

package runtime

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
)

const productionAbsencePath = "/api/owner/v1/expiry-rotation/previews"

type productionAbsenceStaleReader struct{ fixedSelectedWorkspaceReader }

func (r productionAbsenceStaleReader) VerifySelectedWorkspace(context.Context, platform.WorkspaceAccess, string, string) (platform.SelectedWorkspaceFacts, error) {
	facts := r.facts
	facts.Result.ObservedAt = time.Now().UTC().Add(-6 * time.Minute)
	return facts, nil
}

func newProductionAbsenceFixture(t *testing.T) *removalFixture {
	t.Helper()
	f := newJoinFixture(t, 1)
	// Change sources before a NEW preview; never adopt or rebase frozen facts.
	f.exec(t, `DELETE FROM tsw_rotation_usage_ledger WHERE target_account_id=$1`, f.preview.Candidates[0].AccountId)
	return f
}

func productionAbsencePreview(t *testing.T, f *removalFixture) ownerapi.ExpiryRotationPreview {
	t.Helper()
	return rotationResult(t, rotationRequest(f.h, f.session, f.csrf, "POST", productionAbsencePath, nil), 200)
}

func productionAbsenceConfirm(f *removalFixture, p ownerapi.ExpiryRotationPreview) *httptest.ResponseRecorder {
	return rotationRequest(f.h, f.session, f.csrf, "POST", productionAbsencePath+"/"+p.Id.String()+"/confirm", map[string]any{"confirmed": true, "digest": p.Digest, "idempotencyKey": uuid.New(), "assignments": f.preview.Assignments})
}

// Preview/confirmation may write their derived output, auth access and audit;
// all other durable tables, including ledger, protections and epochs, must not change.
func productionAbsenceSources(t *testing.T, f *removalFixture) string {
	t.Helper()
	ctx := context.Background()
	rows, err := f.pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename LIKE 'tsw_%' AND tablename NOT IN ('tsw_expiry_rotation_previews','tsw_owner_sessions','tsw_audit_events') ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err = rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, table := range tables {
		var raw string
		if err = f.pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM `+pgx.Identifier{"public", table}.Sanitize()+` t`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		out.WriteString(table + raw)
	}
	return out.String()
}

func TestExpiryRotationProductionAbsencePreviewConfirm(t *testing.T) {
	f := newProductionAbsenceFixture(t)
	before := productionAbsenceSources(t, f)
	old := f.preview
	p := productionAbsencePreview(t, f)
	if p.Status != "ready" || p.Source != "official_owner_ab" || p.Id == old.Id || p.PolicyVersion == nil || *p.PolicyVersion != 2 || p.Candidates[0].UsageState != "unobserved_prejoin" || p.Candidates[0].Decision != "eligible" || p.Candidates[0].DeliveryStatus != "join_candidate_pending_first_probe" || p.Candidates[0].EverUsed {
		t.Fatalf("official missing-ledger preview not ready: %+v", p)
	}
	// Inspect the actual official producer, not a mock capability override.
	e, err := (officialRotationCapability{handler: f.h}).Evidence(context.Background(), uuid.MustParse(f.owner))
	if err != nil {
		t.Fatal(err)
	}
	v := e.Candidates[p.Candidates[0].AccountId]
	a := v.Usage.Absence
	var generation uuid.UUID
	if err = f.pool.QueryRow(context.Background(), `SELECT session_generation FROM tsw_operation_selection_drafts WHERE owner_id=$1`, f.owner).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if a == nil || a.Source != "persisted_usage_ledger_lookup" || !a.LookupComplete || a.FirstUseRecordStatus != "absent" || a.EvidenceID != rotationAbsenceEvidenceID(*a) || a.EvidenceID == v.Usage.EvidenceID || a.WorkspaceID != p.WorkspaceId || a.AccountID != p.Candidates[0].AccountId || a.MotherID != p.MotherAccountId || a.SessionGeneration != generation || a.VerificationID != p.VerificationId || a.Identifier != p.Candidates[0].Identifier || a.AccountVersion != p.SourceRevisions["account:"+a.AccountID.String()] || a.CredentialVersion != p.SourceRevisions["credential:"+a.AccountID.String()] || a.MembershipVersion != p.SourceRevisions["membership:"+a.AccountID.String()] || !validRotationProof(a.rotationProof, time.Now(), "persisted_usage_ledger_lookup") {
		t.Fatalf("official complete absence proof missing/wrong scope: %+v", a)
	}
	confirmed := rotationResult(t, productionAbsenceConfirm(f, p), 200)
	if !confirmed.Authorized || confirmed.Candidates[0].DeliveryStatus != "join_candidate_pending_first_probe" || before != productionAbsenceSources(t, f) || f.http.calls != 0 || f.http.reads != 0 {
		t.Fatal("producer/confirmation wrote source facts, certified delivery or called platform mutation fixture")
	}
}

func productionAbsenceLedger(t *testing.T, f *removalFixture, state string, other bool) {
	t.Helper()
	workspace := f.space
	if other {
		workspace = uuid.New()
		f.exec(t, `INSERT INTO tsw_workspaces(id,platform_workspace_id,display_name) VALUES($1::uuid,$1::uuid::text,'other')`, workspace)
	}
	observed := time.Now().UTC().Truncate(time.Microsecond)
	if other {
		observed = observed.Add(-24 * time.Hour)
	}
	f.exec(t, `INSERT INTO tsw_rotation_usage_ledger(target_account_id,workspace_id,usage_state,ever_used,evidence_source,evidence_id,observed_at,expires_at) VALUES($1,$2,$3,$4,'fixture',repeat('d',64),$5::timestamptz,$5::timestamptz+interval '1 minute')`, f.preview.Candidates[0].AccountId, workspace, state, state == "used", observed)
}

func TestExpiryRotationProductionAbsenceRejectsUnsafeFacts(t *testing.T) {
	cases := []struct {
		name   string
		change func(*testing.T, *removalFixture)
	}{
		{"unknown_row", func(t *testing.T, f *removalFixture) { productionAbsenceLedger(t, f, "unknown", false) }},
		{"used_row", func(t *testing.T, f *removalFixture) { productionAbsenceLedger(t, f, "used", false) }},
		{"historical_used_other_workspace", func(t *testing.T, f *removalFixture) { productionAbsenceLedger(t, f, "used", true) }},
		{"unknown_other_workspace", func(t *testing.T, f *removalFixture) { productionAbsenceLedger(t, f, "unknown", true) }},
		{"protection", func(t *testing.T, f *removalFixture) {
			f.exec(t, `INSERT INTO tsw_rotation_global_protections(target_account_id,status,evidence_source,evidence_id,observed_at) VALUES($1,'delivered','fixture',repeat('c',64),now())`, f.preview.Candidates[0].AccountId)
		}},
		{"protection_suspected_sold", func(t *testing.T, f *removalFixture) {
			f.exec(t, `INSERT INTO tsw_rotation_global_protections(target_account_id,status,evidence_source,evidence_id,observed_at) VALUES($1,'suspected_sold','fixture',repeat('c',64),now())`, f.preview.Candidates[0].AccountId)
		}},
		{"duplicate_identity", func(t *testing.T, f *removalFixture) {
			f.exec(t, `INSERT INTO tsw_target_accounts(id,identifier,identifier_hmac,identifier_key_version,display_label) SELECT $2,identifier,decode(repeat('ef',32),'hex'),1,'duplicate' FROM tsw_target_accounts WHERE id=$1`, f.preview.Candidates[0].AccountId, uuid.New())
		}},
		{"wrong_identity", func(t *testing.T, f *removalFixture) {
			f.exec(t, `UPDATE tsw_target_accounts SET identifier='wrong@fixture.test',version=version+1 WHERE id=$1`, f.preview.Candidates[0].AccountId)
		}},
		{"missing_material", func(t *testing.T, f *removalFixture) {
			f.exec(t, `UPDATE tsw_target_credentials SET material_status='needs_totp',version=version+1 WHERE target_account_id=$1`, f.preview.Candidates[0].AccountId)
		}},
		{"missing_invitation", func(t *testing.T, f *removalFixture) {
			r := f.h.selectedWorkspaceReader.(fixedSelectedWorkspaceReader)
			r.facts.Result.Members = r.facts.Result.Members[:1]
			zero := 0
			r.facts.Result.PendingInviteCount = &zero
			f.h.selectedWorkspaceReader = r
		}},
		{"wrong_invitation_seat", func(t *testing.T, f *removalFixture) {
			r := f.h.selectedWorkspaceReader.(fixedSelectedWorkspaceReader)
			r.facts.Result.Members[1].SeatType = "default"
			f.h.selectedWorkspaceReader = r
		}},
		{"ambiguous_invitation", func(t *testing.T, f *removalFixture) {
			r := f.h.selectedWorkspaceReader.(fixedSelectedWorkspaceReader)
			invite := r.facts.Result.Members[1]
			invite.SeatType = "default"
			r.facts.Result.Members = append(r.facts.Result.Members, invite)
			two := 2
			r.facts.Result.PendingInviteCount = &two
			f.h.selectedWorkspaceReader = r
		}},
		{"partial_snapshot", func(t *testing.T, f *removalFixture) {
			r := f.h.selectedWorkspaceReader.(fixedSelectedWorkspaceReader)
			two := 2
			r.facts.Result.MemberCount = &two
			f.h.selectedWorkspaceReader = r
		}},
		{"original_usage_unknown", func(t *testing.T, f *removalFixture) {
			f.exec(t, `DELETE FROM tsw_rotation_usage_ledger WHERE target_account_id=$1`, f.preview.Slots[0].AccountId)
		}},
		{"stale_source", func(t *testing.T, f *removalFixture) {
			f.h.selectedWorkspaceReader = productionAbsenceStaleReader{f.h.selectedWorkspaceReader.(fixedSelectedWorkspaceReader)}
		}},
		{"wrong_scope", func(t *testing.T, f *removalFixture) {
			f.exec(t, `UPDATE tsw_operation_selection_drafts SET session_generation=$2,version=version+1 WHERE owner_id=$1`, f.owner, uuid.New())
		}},
		// Faults are confined to this disposable DB fixture, not production schema.
		{"ledger_query_error", func(t *testing.T, f *removalFixture) {
			f.exec(t, `ALTER TABLE tsw_rotation_usage_ledger RENAME TO tsw_fixture_usage_unavailable`)
		}},
		{"protection_query_error", func(t *testing.T, f *removalFixture) {
			// Renaming the underlying table keeps the view's OID reference valid.
			// Remove the name queried by the reader to inject an actual read failure.
			f.exec(t, `ALTER VIEW tsw_rotation_effective_protections RENAME TO tsw_fixture_protection_unavailable`)
		}},
		{"partial_ledger_row", func(t *testing.T, f *removalFixture) {
			productionAbsenceFaultLedger(t, f, "NULL::text", false)
		}},
		{"conflicting_ledger_row", func(t *testing.T, f *removalFixture) {
			productionAbsenceFaultLedger(t, f, "'never_used'::text", true)
		}},
		{"terminal_ledger_error", func(t *testing.T, f *removalFixture) {
			productionAbsenceFaultLedger(t, f, "repeat('x',extract(epoch FROM clock_timestamp()-clock_timestamp())::int / 0)", false)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newProductionAbsenceFixture(t)
			tc.change(t, f)
			before := productionAbsenceSources(t, f)
			p := productionAbsencePreview(t, f)
			if p.Status == "ready" || len(p.Candidates) > 0 && (p.Candidates[0].Decision == "eligible" && tc.name != "original_usage_unknown" || p.Candidates[0].DeliveryStatus != "blocked" && tc.name != "original_usage_unknown") {
				t.Fatalf("unsafe official absence preview: %+v", p)
			}
			if tc.name == "original_usage_unknown" && (p.Slots[0].UsageState != "unknown" || p.Slots[0].Decision != "needs_verification") {
				t.Fatalf("missing original-member usage was reinterpreted: %+v", p.Slots)
			}
			if strings.Contains(tc.name, "error") || tc.name == "partial_ledger_row" {
				if p.Status != "facts_incomplete" {
					t.Fatalf("failed/partial lookup did not remain incomplete: %+v", p)
				}
			}
			if w := productionAbsenceConfirm(f, p); w.Code != 409 {
				t.Fatalf("unsafe confirmation=%d: %s", w.Code, w.Body.String())
			}
			if before != productionAbsenceSources(t, f) || f.http.calls != 0 || f.http.reads != 0 {
				t.Fatal("rejection changed source facts or called platform fixture")
			}
		})
	}
}

// A private fault view adds an other-Workspace row, so the selected lookup
// succeeds with no row before the complete account-history scan encounters it.
func productionAbsenceFaultLedger(t *testing.T, f *removalFixture, stateExpression string, everUsed bool) {
	t.Helper()
	f.exec(t, `ALTER TABLE tsw_rotation_usage_ledger RENAME TO tsw_fixture_usage_partial`)
	ever := "false"
	if everUsed {
		ever = "true"
	}
	f.exec(t, `CREATE VIEW tsw_rotation_usage_ledger AS SELECT target_account_id,workspace_id,usage_state,ever_used,evidence_id,observed_at,expires_at FROM tsw_fixture_usage_partial UNION ALL SELECT '`+f.preview.Candidates[0].AccountId.String()+`'::uuid,'`+uuid.NewString()+`'::uuid,`+stateExpression+`,`+ever+`,repeat('e',64),now(),now()+interval '1 minute'`)
}

func TestExpiryRotationProductionAbsenceOtherWorkspaceNeverUsed(t *testing.T) {
	f := newProductionAbsenceFixture(t)
	productionAbsenceLedger(t, f, "never_used", true)
	p := productionAbsencePreview(t, f)
	if p.Status != "ready" || p.Candidates[0].UsageState != "unobserved_prejoin" {
		t.Fatalf("unrelated Workspace nonblocking evidence prohibited controlled join: %+v", p)
	}
	rotationResult(t, productionAbsenceConfirm(f, p), 200)
}

func TestExpiryRotationProductionAbsenceConfirmDrift(t *testing.T) {
	for _, name := range []string{"used", "unknown", "protection", "identity", "material", "membership", "generation", "expired_source"} {
		t.Run(name, func(t *testing.T) {
			f := newProductionAbsenceFixture(t)
			p := productionAbsencePreview(t, f)
			if p.Status != "ready" {
				t.Fatalf("absence preview not ready: %+v", p)
			}
			var frozen string
			if err := f.pool.QueryRow(context.Background(), `SELECT facts::text||digest FROM tsw_expiry_rotation_previews WHERE id=$1`, p.Id).Scan(&frozen); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "used", "unknown":
				productionAbsenceLedger(t, f, name, true)
			case "protection":
				f.exec(t, `INSERT INTO tsw_rotation_global_protections(target_account_id,status,evidence_source,evidence_id,observed_at) VALUES($1,'delivered','fixture',repeat('c',64),now())`, p.Candidates[0].AccountId)
			case "identity":
				f.exec(t, `UPDATE tsw_target_accounts SET identifier='drift@fixture.test',version=version+1 WHERE id=$1`, p.Candidates[0].AccountId)
			case "material":
				f.exec(t, `UPDATE tsw_target_credentials SET material_status='needs_totp',version=version+1 WHERE target_account_id=$1`, p.Candidates[0].AccountId)
			case "membership":
				f.exec(t, `UPDATE tsw_standby_child_memberships SET version=version+1 WHERE target_account_id=$1`, p.Candidates[0].AccountId)
			case "generation":
				f.exec(t, `UPDATE tsw_operation_selection_drafts SET session_generation=$2,version=version+1 WHERE owner_id=$1`, f.owner, uuid.New())
			case "expired_source":
				f.h.selectedWorkspaceReader = productionAbsenceStaleReader{f.h.selectedWorkspaceReader.(fixedSelectedWorkspaceReader)}
			}
			before := productionAbsenceSources(t, f)
			if w := productionAbsenceConfirm(f, p); w.Code != 409 {
				t.Fatalf("drift confirmation=%d: %s", w.Code, w.Body.String())
			}
			var after string
			if err := f.pool.QueryRow(context.Background(), `SELECT facts::text||digest FROM tsw_expiry_rotation_previews WHERE id=$1 AND status='ready' AND authorized_at IS NULL`, p.Id).Scan(&after); err != nil || after != frozen || before != productionAbsenceSources(t, f) {
				t.Fatalf("rejected confirmation rewrote frozen/source facts: %v", err)
			}
		})
	}
}

func TestExpiryRotationProductionAbsenceConfirmationSourceRace(t *testing.T) {
	for _, protection := range []bool{false, true} {
		name := "usage"
		if protection {
			name = "protection"
		}
		t.Run(name, func(t *testing.T) {
			f := newProductionAbsenceFixture(t)
			p := productionAbsencePreview(t, f)
			if p.Status != "ready" {
				t.Fatalf("absence preview not ready: %+v", p)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err = tx.Exec(ctx, `SELECT version FROM tsw_rotation_epochs WHERE kind='target_account' AND id=$1 FOR UPDATE`, p.Candidates[0].AccountId); err != nil {
				t.Fatal(err)
			}
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() { result <- productionAbsenceConfirm(f, p) }()
			// The official read finishes before confirmation blocks on the epoch.
			for {
				var waiting bool
				if err = f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%tsw_rotation_epochs%' AND pid<>pg_backend_pid())`).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case w := <-result:
					t.Fatalf("confirmation escaped held epoch: %d %s", w.Code, w.Body.String())
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
			query := `INSERT INTO tsw_rotation_usage_ledger(target_account_id,workspace_id,usage_state,ever_used,evidence_source,evidence_id,observed_at,expires_at) VALUES($1,$2,'used',true,'fixture',repeat('f',64),now(),now()+interval '1 minute')`
			args := []any{p.Candidates[0].AccountId, f.space}
			if protection {
				query = `INSERT INTO tsw_rotation_global_protections(target_account_id,status,evidence_source,evidence_id,observed_at) VALUES($1,'delivered','fixture',repeat('f',64),now())`
				args = args[:1]
			}
			if _, err = tx.Exec(ctx, query, args...); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case w := <-result:
				if w.Code != 409 {
					t.Fatalf("source race authorized stale absence: %d %s", w.Code, w.Body.String())
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			var authorized bool
			if err = f.pool.QueryRow(ctx, `SELECT authorized_at IS NOT NULL FROM tsw_expiry_rotation_previews WHERE id=$1`, p.Id).Scan(&authorized); err != nil || authorized {
				t.Fatalf("race committed authorization: %v", err)
			}
		})
	}
}
