//go:build integration

package runtime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/egress"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/migrations"
)

type proxyVerificationRing struct{}

func (proxyVerificationRing) Current() (uint16, [32]byte) { return 1, [32]byte{1, 2, 3} }
func (r proxyVerificationRing) Lookup(version uint16) ([32]byte, bool) {
	_, key := r.Current()
	return key, version == 1
}

func TestProxyPoolIntegrationPageConfigurationLifecycle(t *testing.T) {
	dsn := os.Getenv("TSW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated database required")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() != "127.0.0.1" || !strings.HasPrefix(u.Path, "/tsw_proxy_verify_") {
		t.Fatal("requires an isolated proxy verification database")
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
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ring := proxyVerificationRing{}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/echo" {
			_, _ = w.Write([]byte("1.1.1.1"))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()
	keyVersion, key := ring.Current()
	newManager := func() *egress.Manager {
		router, err := egress.NewManaged("http://platform.test/ready", "http://platform.test/echo", key[:], "1")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(router.CloseIdleConnections)
		leases, err := egress.NewLeaseManager(router, egress.Admission{})
		if err != nil {
			t.Fatal(err)
		}
		return egress.NewManager(leases, router.Status(egress.Admission{}))
	}
	manager := newManager()
	control := newProxyControl(pool, ring, manager)
	view, err := control.view(ctx)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(view)
	if !bytes.Contains(encoded, []byte(`"nodes":[]`)) || view.Total != 0 {
		t.Fatalf("empty pool contract=%s", encoded)
	}
	if _, err := manager.Acquire(ctx); !errors.Is(err, egress.ErrLeaseExhausted) {
		t.Fatalf("empty pool used a route: %v", err)
	}
	view, err = control.importNodes(ctx, ownerapi.ProxyNodeImportRequest{Proxies: []string{"  " + proxy.URL + "  ", proxy.URL}})
	if err != nil {
		t.Fatal(err)
	}
	if view.Total != 1 || view.HealthyCount != 1 {
		t.Fatalf("duplicate import total=%d healthy=%d", view.Total, view.HealthyCount)
	}
	if _, err := control.importNodes(ctx, ownerapi.ProxyNodeImportRequest{Proxies: []string{"http://should-not-save.test:8080", "invalid"}}); err == nil {
		t.Fatal("invalid batch imported")
	}
	view, _ = control.view(ctx)
	if view.Total != 1 {
		t.Fatal("invalid import partially persisted")
	}
	var encrypted, nonce []byte
	var version int
	if err := pool.QueryRow(ctx, `SELECT endpoint_key_version,endpoint_nonce,endpoint_ciphertext FROM tsw_proxy_pool_nodes LIMIT 1`).Scan(&version, &nonce, &encrypted); err != nil {
		t.Fatal(err)
	}
	secret, err := auth.DecryptSecret(uint16(version), nonce, encrypted, ring)
	if err != nil || string(secret) != proxy.URL || bytes.Contains(encrypted, []byte(proxy.URL)) {
		t.Fatal("endpoint encryption failed")
	}
	lease, err := manager.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.removeNode(ctx, view.Nodes[0].Id); !errors.Is(err, egress.ErrNodeInUse) {
		t.Fatalf("active node removal=%v", err)
	}
	if _, err := control.probe(ctx); err != nil {
		t.Fatal(err)
	}
	if got := manager.ActiveNodeCounts()[view.Nodes[0].Id.String()]; got != 1 {
		t.Fatalf("refresh lost active node count=%d", got)
	}
	lease.Release()
	var fetches atomic.Int32
	var unavailable atomic.Bool
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		if unavailable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.URL.Query().Get("token") != "private-test-token" {
			t.Error("source token lost")
		}
		_, _ = w.Write([]byte(proxy.URL))
	}))
	defer source.Close()
	sourceURL := source.URL + "/sub?token=private-test-token"
	view, err = control.putSource(ctx, ownerapi.ProxySourceRequest{Kind: "subscription", Url: &sourceURL, UpdateSeconds: func() *int { value := 300; return &value }()})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(view)
	if bytes.Contains(encoded, []byte("private-test-token")) || view.Source == nil {
		t.Fatal("source was missing or leaked its token")
	}
	if err := pool.QueryRow(ctx, `SELECT secret_key_version,secret_nonce,secret_ciphertext FROM tsw_proxy_pool_sources`).Scan(&version, &nonce, &encrypted); err != nil {
		t.Fatal(err)
	}
	secret, err = auth.DecryptSecret(uint16(version), nonce, encrypted, ring)
	if err != nil || string(secret) != sourceURL || version != int(keyVersion) {
		t.Fatal("subscription encryption failed")
	}
	one, two, four := 1, 2, 4
	view, err = control.updateSettings(ctx, ownerapi.ProxyPoolSettingsRequest{TargetHealthy: &one, ProbeConcurrency: &two, TaskConcurrency: &four})
	if err != nil {
		t.Fatal(err)
	}
	if view.TargetHealthy != 1 || manager.Concurrency() != 4 || view.ProbeConcurrency != 2 {
		t.Fatal("saved settings did not apply")
	}
	before := fetches.Load()
	if _, err := control.maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if fetches.Load() <= before {
		t.Fatal("configured subscription was not refreshed when the healthy target was already met")
	}
	// An unchanged source must preserve node health and its original check time.
	var checkedBefore time.Time
	if err := pool.QueryRow(ctx, `SELECT checked_at FROM tsw_proxy_pool_nodes LIMIT 1`).Scan(&checkedBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := control.putSource(ctx, ownerapi.ProxySourceRequest{Kind: "subscription", UpdateSeconds: func() *int { v := 300; return &v }()}); err != nil {
		t.Fatal(err)
	}
	var checkedAfter time.Time
	if err := pool.QueryRow(ctx, `SELECT checked_at FROM tsw_proxy_pool_nodes LIMIT 1`).Scan(&checkedAfter); err != nil || !checkedAfter.Equal(checkedBefore) {
		t.Fatal("unchanged configuration reprobed healthy node")
	}
	unavailable.Store(true)
	restarted := newManager()
	restored := newProxyControl(pool, ring, restarted)
	if err := restored.restoreSettings(ctx); err != nil {
		t.Fatal(err)
	}
	view, err = restored.maintain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view.HealthyCount != 1 || restarted.Concurrency() != 4 {
		t.Fatal("restart lost persisted nodes/settings")
	}
	lease, err = restarted.Acquire(ctx)
	if err != nil {
		t.Fatalf("stored route unavailable after restart: %v", err)
	}
	lease.Release()
	view, err = restored.updateSettings(ctx, ownerapi.ProxyPoolSettingsRequest{TargetHealthy: &two})
	if err == nil {
		view, err = restored.maintain(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	if fetches.Load() <= before || view.Source.LastError == nil || view.HealthyCount != 1 {
		t.Fatal("refill failure hid stored healthy routes or source error")
	}
	if _, err := restored.removeNode(ctx, view.Nodes[0].Id); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Acquire(ctx); !errors.Is(err, egress.ErrLeaseExhausted) {
		t.Fatalf("removed node remained available: %v", err)
	}
	// Paging and nil-manager settings use the same persisted API data, without network calls.
	stored := newProxyControl(pool, ring, nil)
	proxies := make([]string, 101)
	for i := range proxies {
		proxies[i] = "http://node-" + strings.Repeat("x", i+1) + ".test:8080"
	}
	if _, err := stored.importNodes(ctx, ownerapi.ProxyNodeImportRequest{Proxies: proxies}); err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{20, 50, 100} {
		view, err = stored.view(ctx, 2, size)
		if err != nil {
			t.Fatal(err)
		}
		want := min(size, 101-size)
		if view.Total != 101 || view.Page != 2 || view.PageSize != size || len(view.Nodes) != want {
			t.Fatalf("page size %d: total=%d nodes=%d", size, view.Total, len(view.Nodes))
		}
	}
	if _, err := stored.updateSettings(ctx, ownerapi.ProxyPoolSettingsRequest{ProbeConcurrency: &two}); err != nil {
		t.Fatal(err)
	}
	t.Run("supplier sessions and refill protection", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `DELETE FROM tsw_proxy_pool_nodes`); err != nil {
			t.Fatal(err)
		}
		var mu sync.Mutex
		exits := map[string]int{}
		blocked := false
		duplicate := false
		duplicateIP := 0
		gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			encoded := strings.TrimPrefix(r.Header.Get("Proxy-Authorization"), "Basic ")
			decoded, _ := base64.StdEncoding.DecodeString(encoded)
			user := strings.SplitN(string(decoded), ":", 2)[0]
			if !strings.Contains(user, "-st-North Carolina-city-Charlotte-sid-") {
				w.WriteHeader(407)
				return
			}
			if r.URL.Path == "/echo" {
				if exits[user] == 0 {
					exits[user] = len(exits) + 1
				}
				ip := exits[user]
				if duplicate {
					ip = duplicateIP
				}
				fmt.Fprintf(w, "8.8.4.%d", ip)
				return
			}
			if blocked {
				w.WriteHeader(403)
				return
			}
			w.WriteHeader(200)
		}))
		defer gateway.Close()
		parsed, _ := url.Parse(gateway.URL)
		host, portRaw, _ := net.SplitHostPort(parsed.Host)
		port, _ := strconv.Atoi(portRaw)
		user, password, protocol, country, state, city, sessionType, minutes := "fixture", "secret-fixture", "http", "US", "North Carolina", "Charlotte", "sticky", 5
		request := ownerapi.ProxySourceRequest{Kind: "cliproxy", Host: &host, Port: &port, Protocol: (*ownerapi.ProxySourceRequestProtocol)(&protocol), Country: &country, State: &state, City: &city, Username: &user, Password: &password, SessionType: (*ownerapi.ProxySourceRequestSessionType)(&sessionType), SessionMinutes: &minutes}
		manager := newManager()
		supplier := newProxyControl(pool, ring, manager)
		goal := 3
		if _, err := supplier.updateSettings(ctx, ownerapi.ProxyPoolSettingsRequest{TargetHealthy: &goal}); err != nil {
			t.Fatal(err)
		}
		if _, err := supplier.putSource(ctx, request); err != nil {
			t.Fatal(err)
		}
		view, err := supplier.maintain(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if view.HealthyCount != 3 || view.RefillGenerated != 3 || view.RefillSucceeded != 3 {
			t.Fatalf("refill counts %+v", view)
		}
		checked := len(exits)
		if _, err := supplier.maintain(ctx); err != nil {
			t.Fatal(err)
		}
		if len(exits) != checked {
			t.Fatal("maintenance created unnecessary sessions")
		}
		before := view.Total
		mu.Lock()
		blocked = true
		mu.Unlock()
		diagnostic, err := supplier.testSource(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		if diagnostic.Passed || diagnostic.Steps[len(diagnostic.Steps)-1].HttpStatus != 403 {
			t.Fatal("draft HTTP failure was not classified")
		}
		view, _ = supplier.view(ctx)
		if view.Total != before || view.HealthyCount != 3 {
			t.Fatal("draft changed production pool")
		}
		mu.Lock()
		blocked = false
		mu.Unlock()
		request.Username = nil
		request.Password = nil
		if _, err := supplier.putSource(ctx, request); err != nil {
			t.Fatal("saved supplier secrets were not retained", err)
		}
		lease, err := manager.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		activeID := lease.Candidate().ID
		if _, err := pool.Exec(ctx, `UPDATE tsw_proxy_pool_nodes SET stable_until=now() WHERE id=$1`, activeID); err != nil {
			t.Fatal(err)
		}
		if _, err := supplier.maintain(ctx); err != nil {
			t.Fatal(err)
		}
		var kept bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tsw_proxy_pool_nodes WHERE id=$1 AND retired)`, activeID).Scan(&kept); err != nil || !kept {
			t.Fatal("active expired node was deleted")
		}
		lease.Release()
		if _, err := supplier.maintain(ctx); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tsw_proxy_pool_nodes WHERE id=$1)`, activeID).Scan(&kept); err != nil || kept {
			t.Fatal("released expired session was retained")
		}
		// Duplicate a surviving exit, independent of which original node expired.
		view, err = supplier.view(ctx)
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		for _, node := range view.Nodes {
			if node.State != "healthy" {
				continue
			}
			for username, ip := range exits {
				if strings.Contains(username, "-sid-"+node.SessionKey) {
					duplicateIP = ip
					break
				}
			}
			if duplicateIP != 0 {
				break
			}
		}
		if duplicateIP == 0 {
			mu.Unlock()
			t.Fatal("surviving exit missing")
		}
		duplicate = true
		mu.Unlock()
		goal = 6
		supplier.updateSettings(ctx, ownerapi.ProxyPoolSettingsRequest{TargetHealthy: &goal})
		view, err = supplier.maintain(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if view.HealthyCount != 3 || view.RefillGenerated > 12 || view.Source.LastError == nil {
			t.Fatalf("duplicate exits counted or refill budget exceeded: healthy=%d generated=%d error=%v", view.HealthyCount, view.RefillGenerated, view.Source.LastError)
		}
		generated := view.Total
		view, err = supplier.maintain(ctx)
		if err != nil || view.Total != generated {
			t.Fatal("refill backoff was ignored")
		}
		filtered, err := supplier.filteredView(ctx, 1, 20, "isolated", "cliproxy")
		if err != nil || filtered.Total != 0 || filtered.HealthyCount != 3 {
			t.Fatal("server filters lost independent pool counts")
		}
	})

	t.Run("browser reachability admits proxies and preserves incomplete verification", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `DELETE FROM tsw_proxy_pool_nodes; DELETE FROM tsw_proxy_pool_sources`); err != nil {
			t.Fatal(err)
		}
		gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/echo" {
				_, _ = w.Write([]byte("1.1.1.1"))
				return
			}
			w.Header().Set("Cf-Mitigated", "challenge")
			w.WriteHeader(http.StatusForbidden)
		}))
		defer gateway.Close()
		subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(gateway.URL)) }))
		defer subscription.Close()
		var challenge atomic.Bool
		router, err := egress.New(egress.Config{Mode: egress.ModeRequired, ReachabilityURL: "http://platform.test/", IPEchoURL: "http://platform.test/echo", HMACKey: key[:], HMACKeyVersion: "1", ReachabilityProbe: func(context.Context, *http.Client, string) (egress.ReachabilityResult, error) {
			if challenge.Load() {
				return egress.ReachabilityResult{HTTPStatus: http.StatusForbidden, Code: "proxy_browser_challenge"}, nil
			}
			return egress.ReachabilityResult{HTTPStatus: http.StatusOK}, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		defer router.CloseIdleConnections()
		admission, _ := router.Admit(ctx)
		leases, err := egress.NewLeaseManager(router, admission)
		if err != nil {
			t.Fatal(err)
		}
		manager := egress.NewManager(leases, router.Status(admission))
		browserControl := newProxyControl(pool, ring, manager)
		seconds, address := 300, subscription.URL
		diagnostic, err := browserControl.testSource(ctx, ownerapi.ProxySourceRequest{Kind: "subscription", Url: &address, UpdateSeconds: &seconds})
		if err != nil || !diagnostic.Passed || len(diagnostic.Steps) != 2 {
			t.Fatalf("browser-capable draft rejected: err=%v diagnostic=%+v", err, diagnostic)
		}
		var saved int
		if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM tsw_proxy_pool_sources)+(SELECT count(*) FROM tsw_proxy_pool_nodes)`).Scan(&saved); err != nil || saved != 0 {
			t.Fatal("draft browser test mutated the saved pool")
		}
		view, err := browserControl.importNodes(ctx, ownerapi.ProxyNodeImportRequest{Proxies: []string{gateway.URL}})
		if err != nil || view.HealthyCount != 1 {
			t.Fatalf("browser-capable proxy not admitted: err=%v healthy=%d", err, view.HealthyCount)
		}
		lease, err := manager.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := lease.Remeasure(ctx); err != nil {
			lease.Release()
			t.Fatal(err)
		}
		lease.Release()
		challenge.Store(true)
		view, err = browserControl.probe(ctx)
		if err != nil || view.Total != 1 || view.HealthyCount != 0 || view.PendingCount != 1 || view.Verification == nil || view.Verification.RemovedCount != 0 || view.Verification.RetryCount != 1 {
			t.Fatalf("incomplete browser verification deleted or admitted a proxy: err=%v view=%+v", err, view)
		}
	})

}
