package platform

import (
	"context"
	"time"
)

// SelectedWorkspaceReader reads only from the current saved Personal generation.
// It must not log in, mutate membership, or infer write permission from visibility.
type SelectedWorkspaceReader interface {
	Source() string
	VerifySelectedWorkspace(context.Context, PersonalSession, string, string) (SelectedWorkspaceFacts, error)
}

// ReadEvidence describes one independently attempted capability. Partial reads
// retain the successful and failed endpoint evidence but never imply a complete roster.
type ReadEvidence struct {
	Source       string       `json:"source"`
	ObservedAt   time.Time    `json:"observedAt"`
	Completeness Completeness `json:"completeness"`
	Permission   string       `json:"permission"`
	Outcome      Outcome      `json:"outcome"`
}

type SelectedWorkspaceFacts struct {
	Permission string // manage (fixture-only), read, denied, unknown
	Result     Result // composite becomes verified only when every source is complete
	Sources    []ReadEvidence
}
