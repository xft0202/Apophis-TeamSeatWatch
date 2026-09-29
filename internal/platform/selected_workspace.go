package platform

import "context"

// SelectedWorkspaceReader is an injected, read-only source of management facts.
// There is deliberately no production implementation: Personal discovery and the
// legacy Basic-auth HTTPReader do not prove a Workspace management protocol.
type SelectedWorkspaceReader interface {
	VerifySelectedWorkspace(ctx context.Context, motherAccountID, platformWorkspaceID string) (SelectedWorkspaceFacts, error)
}

type SelectedWorkspaceFacts struct {
	Permission string // manage, read, denied, unknown
	Result     Result // subscription, capacity, invitations and members in one observation
}
