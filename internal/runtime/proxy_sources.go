package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/egress"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/proxyprovider"
)

type savedProxySource struct {
	id         uuid.UUID
	settings   proxyprovider.Settings
	secret     []byte
	enabled    bool
	lastSynced *time.Time
	retryAt    *time.Time
	lastError  string
}

func (p *proxyControl) readSources(ctx context.Context) ([]savedProxySource, error) {
	rows, err := p.pool.Query(ctx, `SELECT id,kind,label,config,secret_key_version,secret_nonce,secret_ciphertext,enabled,last_synced_at,retry_at,last_error FROM tsw_proxy_pool_sources ORDER BY kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []savedProxySource{}
	for rows.Next() {
		var item savedProxySource
		var raw, nonce, ciphertext []byte
		var version int
		var kind, label string
		if err := rows.Scan(&item.id, &kind, &label, &raw, &version, &nonce, &ciphertext, &item.enabled, &item.lastSynced, &item.retryAt, &item.lastError); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &item.settings); err != nil {
			return nil, err
		}
		item.settings.Kind = kind
		item.settings.Label = label
		if item.settings.UpdateSeconds == 0 {
			item.settings.UpdateSeconds = 300
		}
		item.secret, err = auth.DecryptSecret(uint16(version), nonce, ciphertext, p.ring)
		if err != nil {
			item.lastError = "代理凭据无法读取"
			item.secret = nil
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func validateSubscriptionURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || len(value) > 4096 || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return errInvalidProxySource
	}
	return nil
}

func (p *proxyControl) sourceDraft(ctx context.Context, request ownerapi.ProxySourceRequest) (proxyprovider.Settings, []byte, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return proxyprovider.Settings{}, nil, err
	}
	var settings proxyprovider.Settings
	if err = json.Unmarshal(raw, &settings); err != nil {
		return settings, nil, errInvalidProxySource
	}
	settings = proxyprovider.Normalize(settings)
	if err = settings.Validate(); err != nil {
		return settings, nil, errInvalidProxySource
	}
	if settings.Kind == "direct" {
		return settings, nil, nil
	}
	if settings.Kind == "subscription" && request.Url != nil && strings.TrimSpace(*request.Url) != "" {
		if err := validateSubscriptionURL(strings.TrimSpace(*request.Url)); err != nil {
			return settings, nil, err
		}
	}
	var previous []byte
	sources, err := p.readSources(ctx)
	if err != nil {
		return settings, nil, err
	}
	for _, source := range sources {
		if source.settings.Kind == settings.Kind {
			previous = source.secret
			break
		}
	}
	if settings.Kind == "subscription" {
		value := string(previous)
		if request.Url != nil && strings.TrimSpace(*request.Url) != "" {
			value = strings.TrimSpace(*request.Url)
		}
		if err := validateSubscriptionURL(value); err != nil {
			return settings, nil, err
		}
		return settings, []byte(value), nil
	}
	var credentials proxyprovider.Credentials
	if len(previous) > 0 {
		if err := json.Unmarshal(previous, &credentials); err != nil {
			return settings, nil, errInvalidProxySource
		}
	}
	if request.Username != nil && strings.TrimSpace(*request.Username) != "" {
		credentials.Username = strings.TrimSpace(*request.Username)
	}
	if request.Password != nil && *request.Password != "" {
		credentials.Password = *request.Password
	}
	if _, err := proxyprovider.Endpoint(settings, credentials, "validation"); err != nil {
		return settings, nil, errInvalidProxySource
	}
	secret, err := json.Marshal(credentials)
	return settings, secret, err
}

func (p *proxyControl) putSource(ctx context.Context, request ownerapi.ProxySourceRequest) (ownerapi.ProxyPool, error) {
	p.refresh.Lock()
	defer p.refresh.Unlock()
	settings, secret, err := p.sourceDraft(ctx, request)
	if err != nil {
		return ownerapi.ProxyPool{}, err
	}
	sources, err := p.readSources(ctx)
	if err != nil {
		return ownerapi.ProxyPool{}, err
	}
	changed := settings.Kind != "direct"
	routing := settings
	routing.Label = ""
	routing.UpdateSeconds = 0
	for _, source := range sources {
		if source.enabled {
			saved := source.settings
			saved.Label = ""
			saved.UpdateSeconds = 0
			changed = saved != routing || !bytes.Equal(source.secret, secret)
			break
		}
	}
	var version uint16
	var nonce, ciphertext []byte
	if settings.Kind != "direct" {
		version, nonce, ciphertext, err = auth.EncryptSecret(secret, p.ring)
		if err != nil {
			return ownerapi.ProxyPool{}, err
		}
	}
	config, err := json.Marshal(settings)
	if err != nil {
		return ownerapi.ProxyPool{}, err
	}
	err = pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		mode := "proxy_required"
		if settings.Kind == "direct" {
			mode = "direct"
		}
		if _, err := tx.Exec(ctx, `UPDATE tsw_proxy_pool_settings SET routing_mode=$1,updated_at=now() WHERE id=true`, mode); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE tsw_proxy_pool_sources SET enabled=false`); err != nil {
			return err
		}
		if settings.Kind != "direct" {
			_, err := tx.Exec(ctx, `INSERT INTO tsw_proxy_pool_sources(id,kind,label,config,secret_key_version,secret_nonce,secret_ciphertext,enabled) VALUES($1,$2,$3,$4,$5,$6,$7,true) ON CONFLICT(kind) DO UPDATE SET label=excluded.label,config=excluded.config,secret_key_version=excluded.secret_key_version,secret_nonce=excluded.secret_nonce,secret_ciphertext=excluded.secret_ciphertext,enabled=true,last_error='',retry_at=NULL,last_synced_at=CASE WHEN $8 THEN NULL ELSE tsw_proxy_pool_sources.last_synced_at END,updated_at=now()`, uuid.New(), settings.Kind, settings.Label, config, version, nonce, ciphertext, changed)
			if err != nil {
				return err
			}
		}
		if !changed {
			return nil
		}
		_, err := tx.Exec(ctx, `UPDATE tsw_proxy_pool_nodes SET retired=true,state='isolated',failure_reason='proxy_source_changed' WHERE source_id IS NOT NULL`)
		return err
	})
	if err != nil {
		return ownerapi.ProxyPool{}, err
	}
	if settings.Kind == "direct" {
		p.egress.SetMode(egress.ModeDirect)
		p.notify()
		return p.view(ctx)
	}
	p.egress.SetMode(egress.ModeRequired)
	p.force.Store(true)
	p.notify()
	return p.verifyLocked(ctx, false, "")
}

func proxyDiagnostic(steps []egress.ProbeStep, passed bool) ownerapi.ProxySourceDiagnostic {
	result := ownerapi.ProxySourceDiagnostic{Passed: passed, Steps: []ownerapi.ProxyDiagnosticStep{}}
	for _, step := range steps {
		result.Steps = append(result.Steps, ownerapi.ProxyDiagnosticStep{Stage: step.Stage, Code: step.Code, HttpStatus: step.HTTPStatus, DurationMs: step.DurationMs})
	}
	return result
}

func (p *proxyControl) testSource(ctx context.Context, request ownerapi.ProxySourceRequest) (ownerapi.ProxySourceDiagnostic, error) {
	settings, secret, err := p.sourceDraft(ctx, request)
	if err != nil {
		return ownerapi.ProxySourceDiagnostic{}, err
	}
	var endpoint egress.Endpoint
	if settings.Kind == "direct" {
		steps, passed := p.egress.DiagnoseDirect(ctx)
		return proxyDiagnostic(steps, passed), nil
	}
	if settings.Kind == "subscription" {
		started := time.Now()
		body, err := fetchSubscription(ctx, string(secret))
		candidates := parseSubscription(body)
		if err != nil || len(candidates) == 0 || len(candidates) > 1000 {
			var rejected subscriptionStatusError
			status := 0
			if errors.As(err, &rejected) {
				status = rejected.status
			}
			return proxyDiagnostic([]egress.ProbeStep{{Stage: "subscription", Code: "proxy_subscription_unavailable", HTTPStatus: status, DurationMs: time.Since(started).Milliseconds()}}, false), nil
		}
		endpoint = egress.Endpoint{ID: "draft", URL: candidates[0]}
	} else {
		var credentials proxyprovider.Credentials
		if err := json.Unmarshal(secret, &credentials); err != nil {
			return ownerapi.ProxySourceDiagnostic{}, err
		}
		session, err := proxyprovider.SessionKey()
		if err != nil {
			return ownerapi.ProxySourceDiagnostic{}, err
		}
		raw, err := proxyprovider.Endpoint(settings, credentials, session)
		if err != nil {
			return ownerapi.ProxySourceDiagnostic{}, err
		}
		endpoint = egress.Endpoint{ID: "draft", URL: raw, Rotating: settings.SessionType == "rotating"}
	}
	steps, passed := p.egress.Diagnose(ctx, endpoint)
	return proxyDiagnostic(steps, passed), nil
}

func (p *proxyControl) syncSubscription(ctx context.Context, source savedProxySource) error {
	body, err := fetchSubscription(ctx, string(source.secret))
	if err != nil {
		return errors.New("订阅读取失败，请测试来源连接")
	}
	candidates := parseSubscription(body)
	if len(candidates) == 0 || len(candidates) > 1000 {
		return errors.New("订阅没有可用的 HTTP 或 SOCKS5 地址")
	}
	digests := make([][]byte, 0, len(candidates))
	for _, raw := range candidates {
		digest := sha256.Sum256([]byte(egress.NormalizeEndpointURL(raw)))
		digests = append(digests, digest[:])
	}
	return pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE tsw_proxy_pool_nodes SET retired=true,state='isolated',failure_reason='proxy_subscription_removed' WHERE source_id=$1 AND NOT(endpoint_digest=ANY($2::bytea[]))`, source.id, digests); err != nil {
			return err
		}
		for _, raw := range candidates {
			raw = egress.NormalizeEndpointURL(raw)
			scheme, host, err := parseManagedProxy(raw)
			if err != nil {
				return err
			}
			if err = p.saveNode(ctx, tx, source.id, raw, scheme, host, "", "", nil, "sticky"); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE tsw_proxy_pool_sources SET last_synced_at=now(),last_error='',retry_at=NULL WHERE id=$1`, source.id)
		return err
	})
}
