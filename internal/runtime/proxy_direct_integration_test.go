//go:build integration

package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/egress"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/migrations"
)

func TestProxyDirectRoutingPersistenceAndUnusableCleanup(t *testing.T) {
	dsn := os.Getenv("TSW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated database required")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() != "127.0.0.1" || !strings.HasPrefix(u.Path, "/tsw_proxy_verify_") {
		t.Fatal("requires isolated proxy database")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "direct") }))
	defer direct.Close()
	var status atomic.Int32
	status.Store(200)
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code := status.Load(); code != 200 {
			w.WriteHeader(int(code))
			return
		}
		if r.URL.Path == "/echo" {
			_, _ = io.WriteString(w, "1.1.1.1")
			return
		}
		_, _ = io.WriteString(w, "proxy")
	}))
	defer good.Close()
	newControl := func() (*proxyControl, *egress.Manager) {
		router, err := egress.NewManaged(direct.URL, "http://fixture.test/echo", []byte("direct-key"), "1")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(router.CloseIdleConnections)
		leases, _ := egress.NewLeaseManager(router, egress.Admission{})
		manager := egress.NewManager(leases, router.Status(egress.Admission{}))
		control := newProxyControl(pool, proxyVerificationRing{}, manager)
		if err := control.restoreSettings(ctx); err != nil {
			t.Fatal(err)
		}
		return control, manager
	}
	control, manager := newControl()
	view, err := control.view(ctx)
	if err != nil || string(view.Mode) != "direct" || manager.Status().Policy != egress.ModeDirect {
		t.Fatalf("default mode=%s error=%v", view.Mode, err)
	}
	lease, err := manager.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	response, err := lease.Client().Get(direct.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	lease.Release()
	if string(body) != "direct" {
		t.Fatal("default task used proxy")
	}
	view, err = control.importNodes(ctx, ownerapi.ProxyNodeImportRequest{Proxies: []string{good.URL}})
	if err != nil || view.Total != 1 || manager.Status().Policy != egress.ModeDirect {
		t.Fatalf("manual import changed direct mode: %v", err)
	}
	node := view.Nodes[0].Id
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(407) }))
	defer bad.Close()
	view, err = control.importNodes(ctx, ownerapi.ProxyNodeImportRequest{Proxies: []string{bad.URL}})
	if err != nil || view.Total != 1 || view.Verification == nil || view.Verification.RemovedCount != 1 || view.Verification.Diagnostics[0].HttpStatus != 407 {
		t.Fatalf("bad node retained or feedback lost: %+v err=%v", view.Verification, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tsw_proxy_pool_nodes`).Scan(&count); err != nil || count != 1 {
		t.Fatal("unusable manual node retained in storage")
	}
	temporary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(429) }))
	defer temporary.Close()
	view, err = control.importNodes(ctx, ownerapi.ProxyNodeImportRequest{Proxies: []string{temporary.URL}})
	if err != nil || view.Verification == nil || view.Verification.RetryCount != 1 || view.Total != 2 {
		t.Fatal("temporary throttling deleted credentials")
	}
	for _, example := range []struct {
		name   string
		status int
		remove bool
	}{{"confirmed platform rejection", 403, true}, {"request timeout", 408, false}, {"throttled", 429, false}, {"platform unavailable", 503, false}, {"missing diagnostic evidence", 0, false}} {
		t.Run(example.name, func(t *testing.T) {
			id := uuid.New()
			diagnostics, _ := json.Marshal([]egress.ProbeStep{{Stage: "platform", Code: "proxy_target_http_rejected", HTTPStatus: example.status}})
			if _, err := pool.Exec(ctx, `INSERT INTO tsw_proxy_pool_nodes(id,endpoint_key_version,endpoint_nonce,endpoint_ciphertext,endpoint_digest,scheme,display_host,state,failure_reason,diagnostics)
				SELECT $1,endpoint_key_version,endpoint_nonce,endpoint_ciphertext,$2,scheme,display_host,'isolated','proxy_target_http_rejected',$3 FROM tsw_proxy_pool_nodes WHERE id=$4`, id, []byte(id.String()), diagnostics, node); err != nil {
				t.Fatal(err)
			}
			if _, err := control.maintain(ctx); err != nil {
				t.Fatal(err)
			}
			var exists bool
			if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tsw_proxy_pool_nodes WHERE id=$1)`, id).Scan(&exists); err != nil || exists == example.remove {
				t.Fatalf("stored rejection cleanup: exists=%t remove=%t err=%v", exists, example.remove, err)
			}
			if _, err := pool.Exec(ctx, `DELETE FROM tsw_proxy_pool_nodes WHERE id=$1`, id); err != nil {
				t.Fatal(err)
			}
		})
	}
	manager.SetMode(egress.ModeRequired)
	proxyLease, err := manager.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer proxyLease.Release()
	status.Store(407)
	view, err = control.probeNode(ctx, node)
	if err != nil || view.Total != 1 {
		t.Fatalf("unusable active node visible: total=%d err=%v", view.Total, err)
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tsw_proxy_pool_nodes WHERE id=$1 AND retired)`, node).Scan(&exists); err != nil || !exists {
		t.Fatal("active route deleted early")
	}
	proxyLease.Release()
	if err := control.retireLocked(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tsw_proxy_pool_nodes WHERE id=$1)`, node).Scan(&exists); err != nil || exists {
		t.Fatal("released unusable node not deleted")
	}
	status.Store(200)
	subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, good.URL) }))
	defer subscription.Close()
	endpoint, seconds := subscription.URL, 60
	view, err = control.putSource(ctx, ownerapi.ProxySourceRequest{Kind: ownerapi.ProxySourceRequestKindSubscription, Url: &endpoint, UpdateSeconds: &seconds})
	if err != nil || string(view.Mode) != "proxy_required" || manager.Status().Policy != egress.ModeRequired {
		t.Fatal("saved subscription did not switch routes")
	}
	if _, err := control.maintain(ctx); err != nil {
		t.Fatal(err)
	}
	var ciphertext []byte
	if err := pool.QueryRow(ctx, `SELECT secret_ciphertext FROM tsw_proxy_pool_sources WHERE enabled`).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	view, err = control.putSource(ctx, ownerapi.ProxySourceRequest{Kind: ownerapi.ProxySourceRequestKindDirect})
	if err != nil || string(view.Mode) != "direct" {
		t.Fatal("explicit direct mode not saved")
	}
	result, err := control.testSource(ctx, ownerapi.ProxySourceRequest{Kind: ownerapi.ProxySourceRequestKindDirect})
	if err != nil || !result.Passed || manager.Status().Policy != egress.ModeDirect {
		t.Fatal("direct draft altered production routing")
	}
	restored, restarted := newControl()
	if _, err := restored.maintain(ctx); err != nil || restarted.Status().Policy != egress.ModeDirect {
		t.Fatal("direct mode lost after restart")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tsw_proxy_pool_nodes`).Scan(&count); err != nil || count != 1 {
		t.Fatal("direct maintenance deleted transient node")
	}
	var retained []byte
	if err := pool.QueryRow(ctx, `SELECT secret_ciphertext FROM tsw_proxy_pool_sources WHERE NOT enabled`).Scan(&retained); err != nil || string(retained) != string(ciphertext) {
		t.Fatal("direct mode deleted saved subscription credentials")
	}
}
