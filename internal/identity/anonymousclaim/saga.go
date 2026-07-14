// Package anonymousclaim defines the durable claim saga transitions. Storage
// adapters must apply Advance with compare-and-swap on Version.
package anonymousclaim

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"
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

type DeletionReceipt struct {
	ID       string
	Surface  string
	Hash     string
	ErasedAt time.Time
	Details  json.RawMessage
}

type Saga struct {
	ID                       string
	AnonymousSubjectID       string
	EphemeralUserID          string
	OnboardingSessionID      string
	SourceRouteRevisionID    string
	ClaimSetHash             string
	Status                   Status
	Version                  uint64
	ClaimKey                 string
	TargetTenantID           string
	TargetUserID             string
	MissionID                string
	DestinationCommitEventID string
	ReservedAt               time.Time
	ExpiresAt                time.Time
	DeletionReceipts         []DeletionReceipt
}

type Input struct {
	ExpectedVersion          uint64
	Command                  Command
	ClaimKey                 string
	TargetTenantID           string
	TargetUserID             string
	MissionID                string
	DestinationCommitEventID string
	DeletionReceipt          DeletionReceipt
	OccurredAt               time.Time
}

func RequiredDeletionSurfaces() []string { return slices.Clone(requiredReceipts) }

func HasDeletionReceipt(saga Saga, surface string) bool {
	return slices.ContainsFunc(saga.DeletionReceipts, func(receipt DeletionReceipt) bool { return receipt.Surface == surface })
}

// ValidateSuccessor prevents storage adapters from accepting a caller-crafted
// state mutation that did not pass through the state machine.
func ValidateSuccessor(current, successor Saga) error {
	if successor.ID != current.ID || successor.AnonymousSubjectID != current.AnonymousSubjectID || successor.Version != current.Version+1 {
		return ErrInvariant
	}
	input := Input{ExpectedVersion: current.Version}
	switch {
	case current.Status == Available && successor.Status == Reserved:
		input.Command, input.ClaimKey, input.TargetTenantID, input.TargetUserID, input.MissionID = Reserve, successor.ClaimKey, successor.TargetTenantID, successor.TargetUserID, successor.MissionID
		input.OccurredAt = successor.ReservedAt
	case current.Status == Reserved && successor.Status == DestinationCommitted:
		input.Command, input.MissionID, input.DestinationCommitEventID = CommitDestination, successor.MissionID, successor.DestinationCommitEventID
	case current.Status == DestinationCommitted && successor.Status == Erasing:
		input.Command = BeginErasing
	case current.Status == Erasing && successor.Status == Erasing:
		input.Command = RecordDeletionReceipt
		for _, receipt := range successor.DeletionReceipts {
			if !HasDeletionReceipt(current, receipt.Surface) {
				input.DeletionReceipt = receipt
				break
			}
		}
	case current.Status == Erasing && successor.Status == Claimed:
		input.Command = Complete
	case current.Status == Available && successor.Status == Expired:
		input.Command, input.OccurredAt = Expire, current.ExpiresAt
	case successor.Status == ManualReview:
		input.Command = Escalate
	default:
		return ErrInvalidTransition
	}
	expected, err := Advance(current, input)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(expected, successor) {
		return ErrInvariant
	}
	return nil
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
		if input.ClaimKey == "" || (current.ClaimKey != "" && current.ClaimKey != input.ClaimKey) || input.TargetTenantID == "" || input.TargetUserID == "" || input.MissionID == "" || input.OccurredAt.IsZero() {
			return Saga{}, ErrInvariant
		}
		if !current.ExpiresAt.IsZero() && !input.OccurredAt.Before(current.ExpiresAt) {
			return Saga{}, ErrInvalidTransition
		}
		next.Status = Reserved
		next.ClaimKey = input.ClaimKey
		next.TargetTenantID = input.TargetTenantID
		next.TargetUserID = input.TargetUserID
		next.MissionID = input.MissionID
		next.ReservedAt = input.OccurredAt.UTC()
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
		receipt := input.DeletionReceipt
		if !slices.Contains(requiredReceipts, receipt.Surface) || receipt.ID == "" || receipt.Hash == "" || receipt.ErasedAt.IsZero() || !validDetails(receipt.Details) {
			return Saga{}, ErrInvariant
		}
		if existingIndex := slices.IndexFunc(next.DeletionReceipts, func(existing DeletionReceipt) bool { return existing.Surface == receipt.Surface }); existingIndex >= 0 {
			if next.DeletionReceipts[existingIndex].ID != receipt.ID || next.DeletionReceipts[existingIndex].Hash != receipt.Hash || !next.DeletionReceipts[existingIndex].ErasedAt.Equal(receipt.ErasedAt) || string(next.DeletionReceipts[existingIndex].Details) != string(receipt.Details) {
				return Saga{}, ErrInvariant
			}
			return current, nil
		} else {
			next.DeletionReceipts = append(slices.Clone(next.DeletionReceipts), receipt)
		}
	case Complete:
		if current.Status != Erasing {
			return Saga{}, ErrInvalidTransition
		}
		for _, receipt := range requiredReceipts {
			if !HasDeletionReceipt(current, receipt) {
				return Saga{}, fmt.Errorf("%w: missing %s", ErrInvariant, receipt)
			}
		}
		next.Status = Claimed
	case Expire:
		if current.Status != Available {
			return Saga{}, ErrInvalidTransition
		}
		if current.ExpiresAt.IsZero() || input.OccurredAt.IsZero() || input.OccurredAt.Before(current.ExpiresAt) {
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

func validDetails(details json.RawMessage) bool {
	if len(details) == 0 || !json.Valid(details) {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(details, &object) == nil && object != nil
}
