// Package writerfence exposes the durable Ticket10 epoch lock seam. It does not
// grant rotation authorization: not every fact writer/protection source is fenced.
package writerfence

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Kind string

const (
	Owner         Kind = "owner"
	Workspace     Kind = "workspace"
	Mother        Kind = "mother"
	TargetAccount Kind = "target_account"
	StandbyBatch  Kind = "standby_batch"
	Destination   Kind = "destination"
)

type Key struct {
	Kind Kind
	ID   uuid.UUID
}
type Version struct {
	Key     Key
	Version int64
}

// BeginLocked starts a fresh READ COMMITTED transaction and locks every supplied
// epoch in (kind,id) order before returning versions. A missing key fails closed.
// The caller must roll back or commit the returned transaction; re-read facts in
// this transaction AFTER the locks. Never perform network I/O while holding it.
// A higher layer must verify that its key set covers all facts before enabling use.
func BeginLocked(ctx context.Context, pool *pgxpool.Pool, keys []Key) (pgx.Tx, []Version, error) {
	if len(keys) == 0 {
		return nil, nil, errors.New("rotation epoch keys are required")
	}
	ordered := append([]Key(nil), keys...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Kind != ordered[j].Kind {
			return ordered[i].Kind < ordered[j].Kind
		}
		return ordered[i].ID.String() < ordered[j].ID.String()
	})
	for i, key := range ordered {
		switch key.Kind {
		case Owner, Workspace, Mother, TargetAccount, StandbyBatch, Destination:
		default:
			return nil, nil, fmt.Errorf("invalid rotation epoch kind %q", key.Kind)
		}
		if key.ID == uuid.Nil || i > 0 && ordered[i-1] == key {
			return nil, nil, fmt.Errorf("invalid or duplicate rotation epoch key %s/%s", key.Kind, key.ID)
		}
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, nil, err
	}
	versions := make([]Version, 0, len(ordered))
	for _, key := range ordered {
		var version int64
		if err = tx.QueryRow(ctx, `SELECT version FROM tsw_rotation_epochs WHERE kind=$1 AND id=$2 FOR UPDATE`, key.Kind, key.ID).Scan(&version); err != nil {
			_ = tx.Rollback(ctx)
			return nil, nil, fmt.Errorf("lock rotation epoch %s/%s: %w", key.Kind, key.ID, err)
		}
		versions = append(versions, Version{key, version})
	}
	return tx, versions, nil
}
