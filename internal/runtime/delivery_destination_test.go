package runtime

import (
	"context"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

func TestValidateDestinationRejectsUnsafeEndpointsAndGroupIDs(t *testing.T) {
	if _, _, _, ok := validateDestination("channel", "https://hub.example.test/api/v1", "42", "hub-key"); !ok {
		t.Fatal("valid Hub destination rejected")
	}
	for _, endpoint := range []string{
		"http://hub.example.test/api/v1", "https://user:password@hub.example.test/api/v1",
		"https://hub.example.test/api/v1?token=secret", "https://hub.example.test/api/v1#fragment",
		"https://hub.example.test/api/v2", "https://hub.example.test/api/v1/admin/groups", "not-a-url",
	} {
		if _, _, _, ok := validateDestination("channel", endpoint, "42", "hub-key"); ok {
			t.Fatalf("unsafe endpoint accepted: %q", endpoint)
		}
	}
	for _, group := range []string{"", "0", "-1", "042", "team-seatwatch", "9223372036854775808"} {
		if _, _, _, ok := validateDestination("channel", "https://hub.example.test/api/v1", group, "hub-key"); ok {
			t.Fatalf("invalid group ID accepted: %q", group)
		}
	}
	for _, secret := range []string{"", "   "} {
		if _, _, _, ok := validateDestination("channel", "https://hub.example.test/api/v1", "42", secret); ok {
			t.Fatal("empty Hub key accepted")
		}
	}
}

func TestSub2APIProbeReadOnlyGroupValidation(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		connection ownerapi.DeliveryDestinationTestConnection
		target     ownerapi.DeliveryDestinationTestTarget
	}{
		{"success", 200, `{"code":0,"message":"ok","data":[{"id":42,"name":"selected"},{"id":43,"name":"other"}]}`, "connected", "connected"},
		{"missing target", 200, `{"code":0,"data":[{"id":43,"name":"other"}]}`, "connected", "target_mismatch"},
		{"unauthorized", 401, `{"code":401}`, "permission_denied", "permission_denied"},
		{"forbidden", 403, `{"code":403}`, "permission_denied", "permission_denied"},
		{"server error", 500, `{"code":0,"data":[{"id":42,"name":"selected"}]}`, "connection_failed", "untested"},
		{"nonzero code", 200, `{"code":1,"data":[{"id":42,"name":"selected"}]}`, "connection_failed", "untested"},
		{"permission code", 200, `{"code":403,"data":null}`, "permission_denied", "permission_denied"},
		{"missing code", 200, `{"data":[{"id":42,"name":"selected"}]}`, "connection_failed", "untested"},
		{"bad group", 200, `{"code":0,"data":[{"id":"42","name":"selected"}]}`, "connection_failed", "untested"},
		{"null groups", 200, `{"code":0,"data":null}`, "connection_failed", "untested"},
		{"malformed", 200, `not json`, "connection_failed", "untested"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/admin/groups" || r.URL.RawQuery != "" || r.Header.Get("x-api-key") != "hub-key" || r.ContentLength > 0 {
					t.Errorf("probe sent unexpected request: %s %s", r.Method, r.URL)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			probe := NewSub2APIProbe(Sub2APIProbeConfig{AllowedPrivateHosts: []string{"127.0.0.1"}, RootCAs: roots})
			connection, target := probe.ProbeDestination(context.Background(), server.URL+"/api/v1", "42", "hub-key")
			if connection != tc.connection || target != tc.target {
				t.Fatalf("got %s/%s, want %s/%s", connection, target, tc.connection, tc.target)
			}
			if requests.Load() != 1 {
				t.Fatalf("requests=%d, want one read-only GET", requests.Load())
			}
		})
	}
}

func TestSub2APIProbeRejectsPrivateDNSAndRedirects(t *testing.T) {
	var dialCount atomic.Int32
	probe := NewSub2APIProbe(Sub2APIProbeConfig{Resolver: fixedHubResolver{}, Dialer: countingHubDialer{count: &dialCount}})
	connection, target := probe.ProbeDestination(context.Background(), "https://hub.example.test/api/v1", "42", "hub-key")
	if connection != "connection_failed" || target != "untested" || dialCount.Load() != 0 {
		t.Fatalf("private DNS was dialed: %s/%s count=%d", connection, target, dialCount.Load())
	}
	probe = NewSub2APIProbe(Sub2APIProbeConfig{Resolver: mixedHubResolver{}, Dialer: countingHubDialer{count: &dialCount}})
	connection, target = probe.ProbeDestination(context.Background(), "https://hub.example.test/api/v1", "42", "hub-key")
	if connection != "connection_failed" || target != "untested" || dialCount.Load() != 0 {
		t.Fatal("mixed DNS answer was dialed or certified")
	}

	var redirected atomic.Int32
	other := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer other.Close()
	first := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/api/v1/admin/groups", http.StatusFound)
	}))
	defer first.Close()
	roots := x509.NewCertPool()
	roots.AddCert(first.Certificate())
	roots.AddCert(other.Certificate())
	probe = NewSub2APIProbe(Sub2APIProbeConfig{AllowedPrivateHosts: []string{"127.0.0.1"}, RootCAs: roots})
	connection, target = probe.ProbeDestination(context.Background(), first.URL+"/api/v1", "42", "hub-key")
	if connection != "connection_failed" || target != "untested" || redirected.Load() != 0 {
		t.Fatal("redirect followed or certified")
	}
}

type fixedHubResolver struct{}

func (fixedHubResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
}

type mixedHubResolver struct{}

func (mixedHubResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("127.0.0.1")}}, nil
}

type countingHubDialer struct{ count *atomic.Int32 }

func (d countingHubDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	d.count.Add(1)
	return nil, fmt.Errorf("unexpected dial")
}

func TestSub2APIProbeBoundsResponse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, strings.Repeat("x", 1<<20+1))
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	probe := NewSub2APIProbe(Sub2APIProbeConfig{AllowedPrivateHosts: []string{"127.0.0.1"}, RootCAs: roots})
	connection, target := probe.ProbeDestination(context.Background(), server.URL+"/api/v1", "42", "hub-key")
	if connection != "connection_failed" || target != "untested" {
		t.Fatal("oversized response certified")
	}
}
