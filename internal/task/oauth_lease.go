package task

import (
	"context"
	"time"
)

// Browser authorization can outlive one queue lease. Renew the same fence;
// losing it cancels the in-flight grant before it can publish credentials.
func (w *Worker) keepOAuthLease(ctx context.Context, item Task) (context.Context, func(), error) {
	span := w.leaseDuration()
	if err := w.Store.Renew(ctx, item.ID, item.LeaseToken, span); err != nil {
		return nil, nil, err
	}
	bounded, timeout := context.WithTimeout(ctx, 2*time.Minute)
	active, cancel := context.WithCancelCause(bounded)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(max(span/3, time.Millisecond))
		defer ticker.Stop()
		for {
			select {
			case <-active.Done():
				return
			case <-ticker.C:
				renewal, stop := context.WithTimeout(active, min(span/3, 5*time.Second))
				err := w.Store.Renew(renewal, item.ID, item.LeaseToken, span)
				stop()
				if err != nil {
					cancel(err)
					return
				}
			}
		}
	}()
	return active, func() { cancel(context.Canceled); timeout(); <-done }, nil
}
