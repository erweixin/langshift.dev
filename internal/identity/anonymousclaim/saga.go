// Package anonymousclaim defines the durable claim saga transitions. Storage
// adapters must apply Advance with compare-and-swap on Version.
package anonymousclaim

import (
	"errors"
	"fmt"
	"slices"
)

type Status string
type Command string

const (
	Available            Status = "available"
	Reserved             Status = "reserved"
	DestinationCommitted Status = "destination_committed"
	Erasing              Status = "erasing"
	Claimed              Status = "claimed"
	Expired              Status = "expired"
	ManualReview         Status = "manual_review"

	Reserve               Command = "reserve"
	CommitDestination     Command = "commit_destination"
	BeginErasing          Command = "begin_erasing"
	RecordDeletionReceipt Command = "record_deletion_receipt"
	Complete              Command = "complete"
	Expire                Command = "expire"
	Escalate              Command = "escalate"
)

var (
	ErrVersionConflict   = errors.New("anonymous claim version conflict")
	ErrInvalidTransition = errors.New("anonymous claim transition is invalid")
	ErrInvariant         = errors.New("anonymous claim invariant failed")
)

var requiredReceipts = []string{"body_payload", "preview_projection", "principal_mapping"}

type Saga struct {
	ID                       string
	Status                   Status
	Version                  uint64
	ClaimKey                 string
	TargetTenantID           string
	TargetUserID             string
	MissionID                string
	DestinationCommitEventID string
	DeletionReceipts         []string
}

type Input struct {
	ExpectedVersion          uint64
	Command                  Command
	ClaimKey                 string
	TargetTenantID           string
	TargetUserID             string
	MissionID                string
	DestinationCommitEventID string
	DeletionReceipt          string
}

func Advance(current Saga, input Input) (Saga, error) {
	if current.Version != input.ExpectedVersion {
		return Saga{}, ErrVersionConflict
	}
	next := current
	switch input.Command {
	case Reserve:
		if current.Status != Available {
			return Saga{}, ErrInvalidTransition
		}
		if input.ClaimKey == "" || input.TargetTenantID == "" || input.TargetUserID == "" || input.MissionID == "" {
			return Saga{}, ErrInvariant
		}
		next.Status = Reserved
		next.ClaimKey = input.ClaimKey
		next.TargetTenantID = input.TargetTenantID
		next.TargetUserID = input.TargetUserID
		next.MissionID = input.MissionID
	case CommitDestination:
		if current.Status != Reserved {
			return Saga{}, ErrInvalidTransition
		}
		if input.DestinationCommitEventID == "" || input.MissionID != current.MissionID {
			return Saga{}, ErrInvariant
		}
		next.Status = DestinationCommitted
		next.DestinationCommitEventID = input.DestinationCommitEventID
	case BeginErasing:
		if current.Status != DestinationCommitted || current.DestinationCommitEventID == "" {
			return Saga{}, ErrInvalidTransition
		}
		next.Status = Erasing
	case RecordDeletionReceipt:
		if current.Status != Erasing {
			return Saga{}, ErrInvalidTransition
		}
		if !slices.Contains(requiredReceipts, input.DeletionReceipt) {
			return Saga{}, ErrInvariant
		}
		if !slices.Contains(next.DeletionReceipts, input.DeletionReceipt) {
			next.DeletionReceipts = append(slices.Clone(next.DeletionReceipts), input.DeletionReceipt)
		}
	case Complete:
		if current.Status != Erasing {
			return Saga{}, ErrInvalidTransition
		}
		for _, receipt := range requiredReceipts {
			if !slices.Contains(current.DeletionReceipts, receipt) {
				return Saga{}, fmt.Errorf("%w: missing %s", ErrInvariant, receipt)
			}
		}
		next.Status = Claimed
	case Expire:
		if current.Status != Available {
			return Saga{}, ErrInvalidTransition
		}
		next.Status = Expired
	case Escalate:
		if current.Status != Reserved && current.Status != DestinationCommitted && current.Status != Erasing {
			return Saga{}, ErrInvalidTransition
		}
		next.Status = ManualReview
	default:
		return Saga{}, ErrInvalidTransition
	}
	next.Version++
	return next, nil
}
