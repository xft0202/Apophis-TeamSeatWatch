package platform

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"
)

const personalUsageURL = "https://chatgpt.com/backend-api/wham/usage"
const maxPersonalUsageBytes = 64 << 10

// PersonalUsageProbe reads one fixed Personal route using an admitted transport.
// The caller owns the saved session; no password or Workspace account token is accepted.
type PersonalUsageProbe struct{ Client DiscoveryClient }

type personalUsageGuard struct{ next http.RoundTripper }

func (g personalUsageGuard) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet || req.URL == nil || req.URL.String() != personalUsageURL || req.Host != "chatgpt.com" {
		return nil, errors.New("Personal usage request is not the fixed first-party GET")
	}
	return g.next.RoundTrip(req)
}

func (p PersonalUsageProbe) Probe(ctx context.Context, session PersonalSession) (PersonalProbeEvidence, error) {
	if p.Client == nil || !ValidatePersonalRefresh(PersonalRefreshResult{Status: "ready", Session: session}, time.Now()) {
		return PersonalProbeEvidence{}, errors.New("Personal session invalid or expired")
	}
	source, release, err := p.Client(ctx)
	if err != nil {
		return PersonalProbeEvidence{TransportError: err}, nil
	}
	if release != nil {
		defer release()
	}
	if source == nil || source.Transport == nil || source.Timeout <= 0 {
		return PersonalProbeEvidence{TransportError: errors.New("leased client unavailable")}, nil
	}
	client := *source
	client.Jar = nil
	client.Transport = personalUsageGuard{next: source.Transport}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, personalUsageURL, nil)
	if err != nil {
		return PersonalProbeEvidence{}, err
	}
	req.Header.Set("Authorization", "Bearer "+session.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("Originator", "codex_cli_rs")
	req.Header.Set("Version", "0.125.0")
	req.Header.Set("User-Agent", "codex_cli_rs/0.125.0")
	req.Header.Set("X-OpenAI-Target-Path", "/backend-api/wham/usage")
	req.Header.Set("X-OpenAI-Target-Route", "/backend-api/wham/usage")
	resp, err := client.Do(req)
	if err != nil {
		return PersonalProbeEvidence{TransportError: err}, nil
	}
	defer resp.Body.Close()
	evidence := PersonalProbeEvidence{HTTPStatus: resp.StatusCode}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPersonalUsageBytes+1))
	if err != nil || len(body) > maxPersonalUsageBytes {
		evidence.Malformed = true
		return evidence, nil
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusForbidden {
		return evidence, nil
	}
	mediaType, _, mediaErr := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" {
		evidence.Malformed = true
		return evidence, nil
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil || payload == nil {
		evidence.Malformed = true
		return evidence, nil
	}
	if resp.StatusCode == http.StatusForbidden {
		// Only a JSON object with a literal code is verified; free-form text, HTML,
		// and HTTP status alone cannot certify a deactivation.
		for _, key := range []string{"error", "detail"} {
			var nested map[string]json.RawMessage
			if json.Unmarshal(payload[key], &nested) == nil && nested != nil {
				var code string
				if json.Unmarshal(nested["code"], &code) == nil && code == "account_deactivated" {
					evidence.ErrorCode, evidence.VerifiedDeactivation = code, true
					return evidence, nil
				}
			}
		}
		for _, key := range []string{"code", "error_code"} {
			var code string
			if json.Unmarshal(payload[key], &code) == nil && code == "account_deactivated" {
				evidence.ErrorCode, evidence.VerifiedDeactivation = code, true
				break
			}
		}
		return evidence, nil
	}
	var rateLimit map[string]json.RawMessage
	if json.Unmarshal(payload["rate_limit"], &rateLimit) != nil || rateLimit == nil {
		evidence.Malformed = true
		return evidence, nil
	}
	var allowed bool
	verified := json.Unmarshal(rateLimit["allowed"], &allowed) == nil
	for _, key := range []string{"primary_window", "secondary_window"} {
		var window map[string]json.RawMessage
		if json.Unmarshal(rateLimit[key], &window) == nil && window != nil {
			var usedPercent float64
			if json.Unmarshal(window["used_percent"], &usedPercent) == nil && usedPercent >= 0 && usedPercent <= 100 {
				verified = true
			}
		}
	}
	if !verified {
		evidence.Malformed = true
		return evidence, nil
	}
	evidence.VerifiedUsage = true
	return evidence, nil
}
