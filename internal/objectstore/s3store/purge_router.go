package s3store

import (
	"context"
	"errors"
)

// PurgeRouter confines a deletion to one of a fixed set of bucket/prefix
// stores. A reference that matches no configured store is rejected.
type PurgeRouter struct{ Stores []Store }

func (router PurgeRouter) Purge(ctx context.Context, ref string) (PurgeReceipt, error) {
	if len(router.Stores) == 0 {
		return PurgeReceipt{}, ErrConfiguration
	}
	for _, store := range router.Stores {
		receipt, err := store.Purge(ctx, ref)
		if errors.Is(err, ErrReference) {
			continue
		}
		return receipt, err
	}
	return PurgeReceipt{}, ErrReference
}
