package postgres

import (
	"context"
	"errors"
)

var (
	ErrDeliveryConfiguration = errors.New("event delivery configuration is invalid")
	ErrDeliveryConflict      = errors.New("event delivery lease or payload conflict")
	ErrDeliveryBusy          = errors.New("event delivery is already leased")
	ErrStaleStoreEpoch       = errors.New("event delivery store epoch is stale")
	ErrWorkerStoreEpochStale = errors.New("worker store epoch snapshot is stale")
)

// EpochAuthority lives outside the PostgreSQL PITR failure domain. Delivery
// fails closed whenever the authoritative generation cannot be read.
type EpochAuthority interface {
	CurrentStoreEpoch(context.Context) (string, error)
}

func requireCurrentEpoch(ctx context.Context, authority EpochAuthority, expected string) error {
	if authority == nil || expected == "" {
		return ErrDeliveryConfiguration
	}
	current, err := authority.CurrentStoreEpoch(ctx)
	if err != nil || current == "" {
		return ErrDeliveryConfiguration
	}
	if current != expected {
		return ErrStaleStoreEpoch
	}
	return nil
}
