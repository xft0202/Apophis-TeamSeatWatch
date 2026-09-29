package platform

import (
	"context"
	"errors"
	"strings"
)

// DiscoveryAdapter is an explicit access-verification and read-only discovery boundary.
// No production remote login/list contract has been approved; the default never calls a platform.
type DiscoveryAdapter interface {
	VerifyAndDiscover(context.Context, MotherMaterial) (DiscoveryResult, error)
}

type MotherMaterial struct {
	LoginIdentifier string
	Password        string
	TOTPSecret      string
}

type DiscoveredWorkspace struct {
	PlatformID string
	Name       string
	// Access is an observation about readable scope, never an authorization to manage.
	Access string // readable, permission_denied, unknown
}

type DiscoveryResult struct {
	Status     string // discovered, invalid_login, discovery_failed, permission_denied
	Workspaces []DiscoveredWorkspace
}

var ErrDiscoveryUnavailable = errors.New("platform discovery adapter unavailable")

type UnavailableDiscovery struct{}

func (UnavailableDiscovery) VerifyAndDiscover(context.Context, MotherMaterial) (DiscoveryResult, error) {
	return DiscoveryResult{}, ErrDiscoveryUnavailable
}

// ValidateDiscovery rejects contradictory or unbounded adapter responses before persistence.
func ValidateDiscovery(result DiscoveryResult) bool {
	if len(result.Workspaces) > 1000 {
		return false
	}
	if result.Status != "discovered" {
		return len(result.Workspaces) == 0 && (result.Status == "invalid_login" || result.Status == "discovery_failed" || result.Status == "permission_denied")
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
