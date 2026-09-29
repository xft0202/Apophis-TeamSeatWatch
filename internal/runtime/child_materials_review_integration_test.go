//go:build integration

package runtime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/migrations"
)

func childReviewFixture(t *testing.T) (*pgxpool.Pool, *OwnerAuthHandler, string, string, string) {
	t.Helper()
	dsn := os.Getenv("TSW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TSW_TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err = migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ids, session, csrf := seedCardActivationGraph(t, ctx, pool)
	origins, err := auth.ParseOriginPolicy("https://owner.test")
	if err != nil {
		t.Fatal(err)
	}
	return pool, &OwnerAuthHandler{pool: pool, keyRing: cardIntegrationKeyRing{}, origins: origins}, ids.owner, session, csrf
}

func childReviewRequest(t *testing.T, h *OwnerAuthHandler, session, csrf, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	req.Header.Set("Origin", "https://owner.test")
	req.Header.Set(auth.CSRFHeaderName, csrf)
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
	result := httptest.NewRecorder()
	if strings.HasSuffix(path, "/import") {
		h.ImportChildMaterials(result, req, ownerapi.ImportChildMaterialsParams{})
	} else {
		h.ExportChildMaterials(result, req, ownerapi.ExportChildMaterialsParams{})
	}
	return result
}

func childRotatedSession(t *testing.T, result *httptest.ResponseRecorder) string {
	t.Helper()
	for _, cookie := range result.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName {
			return cookie.Value
		}
	}
	t.Fatal("sensitive import did not rotate Owner session")
	return ""
}

func TestChildImportAcceptsLargeTXTAndFilteredLabelExport(t *testing.T) {
	pool, h, _, session, csrf := childReviewFixture(t)
	var text strings.Builder
	for i := 0; i < 650; i++ {
		fmt.Fprintf(&text, "bulk%03d@bulk.test----%s----JBSWY3DPEHPK3PXP\n", i, strings.Repeat("p", 980))
	}
	if text.Len() <= 600<<10 || text.Len() > 10<<20 {
		t.Fatalf("TXT size=%d", text.Len())
	}
	result := childReviewRequest(t, h, session, csrf, "/api/owner/v1/child-materials/import", map[string]string{"content": text.String()})
	if result.Code != 200 {
		t.Fatalf("600KiB TXT rejected: %d %s", result.Code, result.Body.String())
	}
	var imported ownerapi.ChildMaterialsImportResult
	if err := json.Unmarshal(result.Body.Bytes(), &imported); err != nil {
		t.Fatal(err)
	}
	if imported.Imported != 650 {
		t.Fatalf("imported %d want 650", imported.Imported)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE tsw_target_accounts SET display_label='special-label-only' WHERE identifier='bulk000@bulk.test'`); err != nil {
		t.Fatal(err)
	}
	exported := childReviewRequest(t, h, childRotatedSession(t, result), csrf, "/api/owner/v1/child-materials/export", map[string]any{"scope": "filtered", "search": "special-label-only", "expectedCount": 1, "confirmed": true})
	if exported.Code != 200 || !strings.HasPrefix(exported.Body.String(), "bulk000@bulk.test----") || strings.Count(exported.Body.String(), "\n") != 1 {
		t.Fatalf("display-label-only export=%d %s", exported.Code, exported.Body.String())
	}
}

func TestConcurrentChildImportsReportDuplicateWithoutLosingOtherRows(t *testing.T) {
	pool, h, ownerID, first, csrf := childReviewFixture(t)
	second, hash, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(context.Background(), `INSERT INTO tsw_owner_sessions(owner_id,token_hash,auth_version,idle_expires_at,absolute_expires_at) VALUES ($1,$2,1,now()+interval '1 hour',now()+interval '2 hours')`, ownerID, hash[:]); err != nil {
		t.Fatal(err)
	}
	var start sync.WaitGroup
	start.Add(1)
	results := make(chan *httptest.ResponseRecorder, 2)
	for i, session := range []string{first, second} {
		go func(i int, session string) {
			start.Wait()
			shared := "shared@conflict.test----pw----JBSWY3DPEHPK3PXP"
			unique := fmt.Sprintf("unique%d@conflict.test----pw----JBSWY3DPEHPK3PXP", i)
			content := shared + "\n" + unique
			if i == 1 {
				content = unique + "\n" + shared
			}
			results <- childReviewRequest(t, h, session, csrf, "/api/owner/v1/child-materials/import", map[string]string{"content": content})
		}(i, session)
	}
	start.Done()
	duplicates := 0
	for range 2 {
		result := <-results
		if result.Code != 200 {
			t.Fatalf("concurrent import=%d %s", result.Code, result.Body.String())
		}
		var imported ownerapi.ChildMaterialsImportResult
		if err := json.Unmarshal(result.Body.Bytes(), &imported); err != nil {
			t.Fatal(err)
		}
		for _, row := range imported.Rows {
			if row.Identifier == nil {
				t.Fatalf("identifier missing in result: %+v", imported)
			}
			if strings.HasPrefix(*row.Identifier, "unique") && row.Status != "imported" {
				t.Fatalf("unrelated row lost: %+v", imported)
			}
			if *row.Identifier == "shared@conflict.test" && row.Status == "duplicate" {
				duplicates++
			}
		}
	}
	if duplicates != 1 {
		t.Fatalf("common account duplicate feedback in %d imports, want 1", duplicates)
	}
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM tsw_target_accounts WHERE identifier LIKE '%@conflict.test'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("concurrent import left %d rows, want 3", count)
	}
}
