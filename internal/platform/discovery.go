package platform

import (
	"context"
	"errors"
	"strings"
	"time"
)

// PersonalSession is a confirmed Personal credential generation. It must never
// be returned through the Owner API or confused with a Workspace OAuth token.
type PersonalSession struct {
	AccessToken string          `json:"accessToken"`
	Cookies     []SessionCookie `json:"cookies"`
	DeviceID    string          `json:"deviceId"`
	ExpiresAt   time.Time       `json:"expiresAt"`
}

type SessionCookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type PersonalRefreshResult struct {
	Status  string // ready, invalid_login, refresh_failed
	Session PersonalSession
}

// PersonalSessionRefresher is a separate explicit password+TOTP operation.
// The caller supplies canonical materials; refresh never runs during save or probe.
type PersonalSessionRefresher interface {
	RefreshPersonal(context.Context, MotherMaterial) (PersonalRefreshResult, error)
}

type DiscoveryAdapter interface {
	Discover(context.Context, PersonalSession) (DiscoveryResult, error)
}

type MotherMaterial struct {
	LoginIdentifier string
	Password        string
	TOTPSecret      string
}

type DiscoveredWorkspace struct {
	PlatformID string
	Name       string
	// Readable is never a grant of management permission.
	Access string // readable, permission_denied, unknown
}

type DiscoveryResult struct {
	Status     string // discovered, session_expired, discovery_failed, permission_denied
	Workspaces []DiscoveredWorkspace
}

var ErrDiscoveryUnavailable = errors.New("platform discovery adapter unavailable")
var ErrPersonalRefreshUnavailable = errors.New("personal session refresh adapter unavailable")

type UnavailableDiscovery struct{}

func (UnavailableDiscovery) Discover(context.Context, PersonalSession) (DiscoveryResult, error) {
	return DiscoveryResult{}, ErrDiscoveryUnavailable
}

type UnavailablePersonalRefresh struct{}

func (UnavailablePersonalRefresh) RefreshPersonal(context.Context, MotherMaterial) (PersonalRefreshResult, error) {
	return PersonalRefreshResult{}, ErrPersonalRefreshUnavailable
}

func ValidatePersonalRefresh(result PersonalRefreshResult, now time.Time) bool {
	if result.Status != "ready" {
		return (result.Status == "invalid_login" || result.Status == "refresh_failed") && result.Session.AccessToken == "" && len(result.Session.Cookies) == 0 && result.Session.DeviceID == "" && result.Session.ExpiresAt.IsZero()
	}
	s := result.Session
	if s.ExpiresAt.Before(now.Add(time.Minute)) || s.ExpiresAt.After(now.Add(24*time.Hour)) ||
		strings.TrimSpace(s.AccessToken) == "" || len(s.AccessToken) > 16384 || strings.ContainsAny(s.AccessToken, "\r\n\t\x00") ||
		strings.TrimSpace(s.DeviceID) == "" || len(s.DeviceID) > 128 || strings.ContainsAny(s.DeviceID, "\r\n\t\x00") || len(s.Cookies) < 1 || len(s.Cookies) > 50 {
		return false
	}
	hasSessionCookie := false
	for _, cookie := range s.Cookies {
		if strings.TrimSpace(cookie.Name) == "" || len(cookie.Name) > 128 || cookie.Value == "" || len(cookie.Value) > 16384 ||
			strings.ContainsAny(cookie.Name+cookie.Value, "\r\n\t;\x00") {
			return false
		}
		if cookie.Name == "__Secure-next-auth.session-token" {
			hasSessionCookie = true
		}
	}
	return hasSessionCookie
}

// ValidateDiscovery rejects contradictory or unbounded adapter responses before persistence.
func ValidateDiscovery(result DiscoveryResult) bool {
	if len(result.Workspaces) > 1000 {
		return false
	}
	if result.Status != "discovered" {
		return len(result.Workspaces) == 0 && (result.Status == "session_expired" || result.Status == "discovery_failed" || result.Status == "permission_denied")
	}
	seen := make(map[string]bool, len(result.Workspaces))
	for _, item := range result.Workspaces {
		if strings.TrimSpace(item.PlatformID) == "" || len(item.PlatformID) > 255 || strings.TrimSpace(item.Name) == "" || len(item.Name) > 120 ||
			(item.Access != "readable" && item.Access != "permission_denied" && item.Access != "unknown") || seen[item.PlatformID] {
			return false
		}
		seen[item.PlatformID] = true
	}
	return true
}
