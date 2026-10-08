package egress

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
)

// ProbeStep contains classified facts only: no URL, username, password, or raw network error.
type ProbeStep struct {
	Stage      string `json:"stage"`
	Code       string `json:"code"`
	HTTPStatus int    `json:"httpStatus"`
	DurationMs int64  `json:"durationMs"`
}

type probeHTTPError struct {
	status int
	code   string
}

func (e *probeHTTPError) Error() string { return "proxy target returned non-success status" }

func diagnosticCode(err error) (string, int) {
	if err == nil {
		return "", 0
	}
	var status *probeHTTPError
	if errors.As(err, &status) {
		if status.code != "" {
			return status.code, status.status
		}
		if status.status == http.StatusProxyAuthRequired {
			return "proxy_auth_rejected", status.status
		}
		return "proxy_target_http_rejected", status.status
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "proxy_dns_failed", 0
	}
	var network net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &network) && network.Timeout()) {
		return "proxy_timeout", 0
	}
	if errors.Is(err, context.Canceled) {
		return "proxy_cancelled", 0
	}
	var cert x509.UnknownAuthorityError
	var hostname x509.HostnameError
	if errors.As(err, &cert) || errors.As(err, &hostname) {
		return "proxy_tls_failed", 0
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "authentication") || strings.Contains(message, "auth failed") || strings.Contains(message, "no acceptable authentication") {
		return "proxy_auth_rejected", 0
	}
	if strings.Contains(message, "tls") || strings.Contains(message, "certificate") {
		return "proxy_tls_failed", 0
	}
	if code := ErrorCode(err); code != "egress_failure" {
		return code, 0
	}
	return "proxy_connection_failed", 0
}

// VerifyEndpoint measures a draft without modifying the production admission set.
func (r *Router) VerifyEndpoint(ctx context.Context, endpoint Endpoint) (Candidate, []ProbeStep, string) {
	steps := make([]ProbeStep, 0, 3)
	appendStep := func(stage string, started time.Time, err error) string {
		code, status := diagnosticCode(err)
		steps = append(steps, ProbeStep{Stage: stage, Code: code, HTTPStatus: status, DurationMs: time.Since(started).Milliseconds()})
		return code
	}
	started := time.Now()
	parsed, err := parseEndpoint(endpoint.URL)
	if err != nil {
		code := appendStep("configuration", started, err)
		return Candidate{}, steps, code
	}
	if !endpoint.StableUntil.IsZero() && !endpoint.StableUntil.After(time.Now()) {
		code := appendStep("session", started, configError("proxy_session_expired"))
		return Candidate{}, steps, code
	}
	client, err := r.clientForEndpoint(parsed)
	if err != nil {
		code := appendStep("configuration", started, err)
		return Candidate{}, steps, code
	}
	defer func() {
		client.CloseIdleConnections()
		r.mu.Lock()
		if transport, ok := client.Transport.(*http.Transport); ok {
			delete(r.transports, transport)
		}
		r.mu.Unlock()
	}()
	started = time.Now()
	body, err := probeBody(ctx, client, r.config.IPEchoURL)
	if err != nil {
		code := appendStep("exit", started, err)
		return Candidate{}, steps, code
	}
	addr, err := NormalizePublicIP(body)
	if code := appendStep("exit", started, err); code != "" {
		return Candidate{}, steps, code
	}
	started = time.Now()
	if code := appendStep("platform", started, r.probeStatus(ctx, client, r.config.ReachabilityURL)); code != "" {
		return Candidate{}, steps, code
	}
	if endpoint.Rotating {
		code := appendStep("session", time.Now(), configError("proxy_session_unstable"))
		return Candidate{}, steps, code
	}
	return Candidate{ID: endpoint.ID, Scheme: parsed.scheme, Fingerprint: Fingerprint(r.config.HMACKey, r.config.HMACKeyVersion, addr), VerifiedAt: time.Now().UTC(), endpoint: endpoint, Diagnostics: steps}, steps, ""
}

func (m *Manager) Diagnose(ctx context.Context, endpoint Endpoint) ([]ProbeStep, bool) {
	if m == nil || m.router == nil {
		return []ProbeStep{{Stage: "configuration", Code: "proxy_transport_invalid"}}, false
	}
	_, steps, code := m.router.VerifyEndpoint(ctx, endpoint)
	return steps, code == ""
}

// DiagnoseDirect tests the platform with an explicit transport; it does not
// acquire business capacity or change the saved routing policy.
func (m *Manager) DiagnoseDirect(ctx context.Context) ([]ProbeStep, bool) {
	if m == nil || m.router == nil {
		return []ProbeStep{{Stage: "configuration", Code: "proxy_transport_invalid"}}, false
	}
	client := m.router.clientForTransport(m.router.directTransport())
	defer client.CloseIdleConnections()
	defer m.router.forgetClient(client)
	started := time.Now()
	code, status := diagnosticCode(m.router.probeStatus(ctx, client, m.router.config.ReachabilityURL))
	return []ProbeStep{{Stage: "platform", Code: code, HTTPStatus: status, DurationMs: time.Since(started).Milliseconds()}}, code == ""
}
