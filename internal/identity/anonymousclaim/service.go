package anonymousclaim

import (
	"context"
	"errors"
)

var ErrClaimTaken = errors.New("anonymous route is already reserved by another claim")

type Store interface {
	Load(context.Context, string) (Saga, error)
	CompareAndSwap(context.Context, Saga, Saga) error
}

type Service struct{ Store Store }

type Reservation struct {
	ClaimID        string
	ClaimKey       string
	TargetTenantID string
	TargetUserID   string
	MissionID      string
}

// Reserve is idempotent for the same claim binding. A concurrent request with
// a different binding never steals an existing reservation.
func (service Service) Reserve(ctx context.Context, reservation Reservation) (Saga, error) {
	if service.Store == nil || reservation.ClaimID == "" {
		return Saga{}, ErrInvariant
	}
	for attempts := 0; attempts < 8; attempts++ {
		current, err := service.Store.Load(ctx, reservation.ClaimID)
		if err != nil {
			return Saga{}, err
		}
		if current.Status != Available {
			if current.ClaimKey == reservation.ClaimKey && current.TargetTenantID == reservation.TargetTenantID && current.TargetUserID == reservation.TargetUserID && current.MissionID == reservation.MissionID {
				return current, nil
			}
			return Saga{}, ErrClaimTaken
		}
		next, err := Advance(current, Input{ExpectedVersion: current.Version, Command: Reserve, ClaimKey: reservation.ClaimKey, TargetTenantID: reservation.TargetTenantID, TargetUserID: reservation.TargetUserID, MissionID: reservation.MissionID})
		if err != nil {
			return Saga{}, err
		}
		if err = service.Store.CompareAndSwap(ctx, current, next); err == nil {
			return next, nil
		}
		if !errors.Is(err, ErrVersionConflict) {
			return Saga{}, err
		}
	}
	return Saga{}, ErrVersionConflict
}
