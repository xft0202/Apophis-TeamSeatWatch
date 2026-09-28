package egress

import "context"

// LeaseProvider is the narrow, already-verified route required by a platform attempt.
type LeaseProvider interface {
	Acquire(ctx context.Context) (*Lease, error)
	ActiveCount() int
}

// Manager exposes the immutable startup pool and its redacted admission status.
// An Owner request cannot change the deployment's routing policy.
type Manager struct {
	pool   *LeaseManager
	status Status
}

func NewManager(pool *LeaseManager, status Status) *Manager {
	return &Manager{pool: pool, status: status}
}

func (m *Manager) Acquire(ctx context.Context) (*Lease, error) {
	if m.pool == nil {
		return nil, ErrLeaseExhausted
	}
	return m.pool.Acquire(ctx)
}

func (m *Manager) ActiveCount() int {
	if m.pool == nil {
		return 0
	}
	return m.pool.ActiveCount()
}

func (m *Manager) Status() Status { return m.status }
