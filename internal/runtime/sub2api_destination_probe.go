package runtime

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xft0202/Apophis-TeamSeatWatch/internal/egress"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

// Sub2APIProbeConfig contains explicit deployment/test overrides. Production
// defaults reject all non-public addresses, including DNS rebinding at dial time.
// AllowedPrivateHosts is an exact host allowlist, never a suffix or wildcard.
type Sub2APIProbeConfig struct {
	AllowedPrivateHosts []string
	RootCAs             *x509.CertPool
	Resolver            interface {
		LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
	}
	Dialer interface {
		DialContext(context.Context, string, string) (net.Conn, error)
	}
}

type sub2APIProbe struct{ client *http.Client }

func NewSub2APIProbe(config Sub2APIProbeConfig) DestinationProbe {
	allowed := make(map[string]bool, len(config.AllowedPrivateHosts))
	for _, host := range config.AllowedPrivateHosts {
		allowed[strings.ToLower(host)] = true
	}
	resolver := config.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	dialer := config.Dialer
	if dialer == nil {
		dialer = &net.Dialer{Timeout: 5 * time.Second}
	}
	transport := &http.Transport{
		Proxy:                 nil,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		DisableKeepAlives:     true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, errors.New("invalid hub address")
			}
			ips, err := resolver.LookupIPAddr(ctx, host)
			if parsed, parseErr := netip.ParseAddr(host); parseErr == nil {
				ips, err = []net.IPAddr{{IP: net.IP(parsed.AsSlice())}}, nil
			}
			if err != nil || len(ips) == 0 {
				return nil, errors.New("hub DNS lookup failed")
			}
			for _, entry := range ips {
				ip, ok := netip.AddrFromSlice(entry.IP)
				if !ok || (!allowed[strings.ToLower(host)] && !publicHubIP(ip)) {
					return nil, errors.New("hub address is not public")
				}
			}
			var dialErr error
			for _, entry := range ips {
				var conn net.Conn
				conn, dialErr = dialer.DialContext(ctx, network, net.JoinHostPort(entry.IP.String(), port))
				if dialErr == nil {
					return conn, nil
				}
			}
			return nil, dialErr
		},
	}
	if config.RootCAs != nil {
		transport.TLSClientConfig = &tls.Config{RootCAs: config.RootCAs}
	}
	return sub2APIProbe{client: &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func publicHubIP(ip netip.Addr) bool {
	_, err := egress.NormalizePublicIP([]byte(ip.Unmap().String()))
	return err == nil
}

func validSub2APIBase(raw string) (*url.URL, bool) {
	if len(raw) > 2048 || strings.TrimSpace(raw) != raw {
		return nil, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || u.Path != "/api/v1" {
		return nil, false
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, false
		}
	}
	return u, true
}

func (p sub2APIProbe) ProbeDestination(ctx context.Context, endpoint, targetGroup, secret string) (ownerapi.DeliveryDestinationTestConnection, ownerapi.DeliveryDestinationTestTarget) {
	failed, untested := ownerapi.DeliveryDestinationTestConnectionConnectionFailed, ownerapi.DeliveryDestinationTestTargetUntested
	base, ok := validSub2APIBase(endpoint)
	groupID, err := strconv.ParseInt(targetGroup, 10, 64)
	if !ok || err != nil || groupID <= 0 || secret == "" {
		return failed, untested
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String()+"/admin/groups", nil)
	if err != nil {
		return failed, untested
	}
	req.Header.Set("x-api-key", secret)
	req.Header.Set("Accept", "application/json")
	response, err := p.client.Do(req)
	if err != nil {
		return failed, untested
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return ownerapi.DeliveryDestinationTestConnectionPermissionDenied, ownerapi.DeliveryDestinationTestTargetPermissionDenied
	}
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || mediaErr != nil || mediaType != "application/json" {
		return failed, untested
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(body) > 1<<20 {
		return failed, untested
	}
	var envelope struct {
		Code *int            `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Code == nil {
		return failed, untested
	}
	if *envelope.Code == http.StatusUnauthorized || *envelope.Code == http.StatusForbidden {
		return ownerapi.DeliveryDestinationTestConnectionPermissionDenied, ownerapi.DeliveryDestinationTestTargetPermissionDenied
	}
	if *envelope.Code != 0 || len(envelope.Data) == 0 {
		return failed, untested
	}
	var groups []struct {
		ID   *int64  `json:"id"`
		Name *string `json:"name"`
	}
	if err := json.Unmarshal(envelope.Data, &groups); err != nil || groups == nil {
		return failed, untested
	}
	found := false
	for _, group := range groups {
		if group.ID == nil || *group.ID <= 0 || group.Name == nil || strings.TrimSpace(*group.Name) == "" {
			return failed, untested
		}
		if *group.ID == groupID {
			found = true
		}
	}
	if found {
		return ownerapi.DeliveryDestinationTestConnectionConnected, ownerapi.DeliveryDestinationTestTargetConnected
	}
	return ownerapi.DeliveryDestinationTestConnectionConnected, ownerapi.DeliveryDestinationTestTargetTargetMismatch
}
