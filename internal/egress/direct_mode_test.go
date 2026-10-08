package egress

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDirectModeSwitchPreservesRoutesAndGlobalCapacity(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "direct") }))
	defer direct.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/echo" {
			_, _ = io.WriteString(w, "1.1.1.1")
			return
		}
		_, _ = io.WriteString(w, "proxy")
	}))
	defer proxy.Close()
	router, err := NewManaged(direct.URL, "http://exit.test/echo", []byte("direct-mode-key"), "1")
	if err != nil {
		t.Fatal(err)
	}
	defer router.CloseIdleConnections()
	pool, _ := NewLeaseManager(router, Admission{})
	manager := NewManager(pool, router.Status(Admission{}))
	manager.SetConcurrency(1)
	manager.SetRefillSignal(func() {})
	if _, err := manager.Reload(context.Background(), []Endpoint{{ID: "proxy", URL: proxy.URL}}); err != nil {
		t.Fatal(err)
	}
	manager.SetMode(ModeDirect)
	first, err := manager.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	check := func(lease *Lease, expected string) {
		t.Helper()
		response, err := lease.Client().Get(direct.URL)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if string(body) != expected {
			t.Fatalf("route=%s expected=%s", body, expected)
		}
	}
	check(first, "direct")
	manager.SetMode(ModeRequired)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := manager.Acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("active direct lease did not reserve capacity: %v", err)
	}
	check(first, "direct")
	first.Release()
	second, err := manager.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	manager.SetMode(ModeDirect)
	check(second, "proxy")
	second.Release()
	third, err := manager.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	check(third, "direct")
	third.Release()
	third.Release()
	if manager.ActiveCount() != 0 {
		t.Fatal("released direct lease retained capacity")
	}
	manager.SetMode(ModeRequired)
	_, _ = manager.Reload(context.Background(), nil)
	if _, err := manager.Acquire(context.Background()); !errors.Is(err, ErrLeaseExhausted) {
		t.Fatalf("empty proxy mode fell back to direct: %v", err)
	}
	steps, passed := manager.DiagnoseDirect(context.Background())
	if !passed || len(steps) != 1 || manager.Status().Policy != ModeRequired {
		t.Fatal("direct draft changed saved proxy policy")
	}
}

func TestDirectModeConcurrentLeasesStayWithinCeiling(t *testing.T) {
	router, _ := NewManaged("http://platform.test", "http://exit.test", []byte("key"), "1")
	defer router.CloseIdleConnections()
	pool, _ := NewLeaseManager(router, Admission{})
	manager := NewManager(pool, router.Status(Admission{}))
	manager.SetMode(ModeDirect)
	manager.SetConcurrency(2)
	manager.SetRefillSignal(func() {})
	var workers sync.WaitGroup
	var active, peak atomic.Int32
	for range 12 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			lease, err := manager.Acquire(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			n := active.Add(1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			time.Sleep(5 * time.Millisecond)
			active.Add(-1)
			lease.Release()
		}()
	}
	workers.Wait()
	if peak.Load() != 2 || manager.ActiveCount() != 0 {
		t.Fatalf("peak=%d retained=%d", peak.Load(), manager.ActiveCount())
	}
}
