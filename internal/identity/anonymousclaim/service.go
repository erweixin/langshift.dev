package anonymousclaim

import (
	"context"
	"errors"
)

var ErrClaimTaken = errors.New("anonymous route is already reserved by another claim")
var ErrReconcileLimit = errors.New("anonymous claim reconciliation did not converge")

type Store interface {
	Load(context.Context, string) (Saga, error)
	CompareAndSwap(context.Context, Saga, Saga) error
}

// DestinationWriter must make the destination Mission and its commit event
// idempotent by ClaimKey. A timeout after an unknown commit result must be
// reconciled by that same key before returning or issuing another effect.
type DestinationWriter interface {
	CommitDestination(context.Context, Saga) (string, error)
}

// Eraser must make each deletion effect idempotent by (ClaimKey, Surface) and
// return the same durable receipt after an unknown result.
type Eraser interface {
	Erase(context.Context, Saga, string) (DeletionReceipt, error)
}

type Service struct {
	Store       Store
	Destination DestinationWriter
	Eraser      Eraser
}

type Reservation struct {
	ClaimID        string
	ClaimKey       string
	TargetTenantID string
	TargetUserID   string
	MissionID      string
}

// Reconcile advances a reserved claim to a single destination Mission and then
// to complete erasure. Every boundary is persisted with CAS, so process crashes
// only cause an idempotent replay of the current step.
func (service Service) Reconcile(ctx context.Context, claimID string) (Saga, error) {
	if service.Store == nil || service.Destination == nil || service.Eraser == nil || claimID == "" {
		return Saga{}, ErrInvariant
	}
	for attempts := 0; attempts < 32; attempts++ {
		current, err := service.Store.Load(ctx, claimID)
		if err != nil {
			return Saga{}, err
		}
		var next Saga
		switch current.Status {
		case Reserved:
			eventID, effectErr := service.Destination.CommitDestination(ctx, current)
			if effectErr != nil {
				return current, effectErr
			}
			next, err = Advance(current, Input{ExpectedVersion: current.Version, Command: CommitDestination, MissionID: current.MissionID, DestinationCommitEventID: eventID})
		case DestinationCommitted:
			next, err = Advance(current, Input{ExpectedVersion: current.Version, Command: BeginErasing})
		case Erasing:
			missing := ""
			for _, surface := range requiredReceipts {
				if !HasDeletionReceipt(current, surface) {
					missing = surface
					break
				}
			}
			if missing == "" {
				next, err = Advance(current, Input{ExpectedVersion: current.Version, Command: Complete})
			} else {
				receipt, effectErr := service.Eraser.Erase(ctx, current, missing)
				if effectErr != nil {
					return current, effectErr
				}
				next, err = Advance(current, Input{ExpectedVersion: current.Version, Command: RecordDeletionReceipt, DeletionReceipt: receipt})
			}
		case Claimed, Expired, ManualReview:
			return current, nil
		default:
			return current, ErrInvalidTransition
		}
		if err != nil {
			return current, err
		}
		if err = service.Store.CompareAndSwap(ctx, current, next); err == nil {
			if next.Status == Claimed {
				return next, nil
			}
			continue
		}
		if !errors.Is(err, ErrVersionConflict) {
			return current, err
		}
	}
	return Saga{}, ErrReconcileLimit
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
