package platform

import "testing"

func TestValidateDiscoveryRejectsAmbiguousOrInventedWorkspaceFacts(t *testing.T) {
	cases := []struct {
		name   string
		result DiscoveryResult
		want   bool
	}{
		{"multiple", DiscoveryResult{Status: "discovered", Workspaces: []DiscoveredWorkspace{{PlatformID: "team-a", Name: "Team A", Access: "readable"}, {PlatformID: "team-b", Name: "Team B", Access: "permission_denied"}}}, true},
		{"empty", DiscoveryResult{Status: "discovered"}, true},
		{"invalid_login", DiscoveryResult{Status: "invalid_login"}, true},
		{"permission_denied", DiscoveryResult{Status: "permission_denied"}, true},
		{"duplicate", DiscoveryResult{Status: "discovered", Workspaces: []DiscoveredWorkspace{{PlatformID: "team-a", Name: "Team A", Access: "readable"}, {PlatformID: "team-a", Name: "Other", Access: "readable"}}}, false},
		{"failure_with_items", DiscoveryResult{Status: "discovery_failed", Workspaces: []DiscoveredWorkspace{{PlatformID: "team-a", Name: "Team A", Access: "readable"}}}, false},
		{"claims_manageable", DiscoveryResult{Status: "discovered", Workspaces: []DiscoveredWorkspace{{PlatformID: "team-a", Name: "Team A", Access: "manageable"}}}, false},
		{"unknown_status", DiscoveryResult{Status: "success"}, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidateDiscovery(tt.result); got != tt.want {
				t.Fatalf("validate=%t want=%t", got, tt.want)
			}
		})
	}
}

func TestUnavailableDiscoveryFailsClosed(t *testing.T) {
	result, err := (UnavailableDiscovery{}).VerifyAndDiscover(t.Context(), MotherMaterial{LoginIdentifier: "mother@example.test", Password: "secret"})
	if err != ErrDiscoveryUnavailable || len(result.Workspaces) != 0 || result.Status != "" {
		t.Fatalf("default adapter must not invent access: %+v %v", result, err)
	}
}
