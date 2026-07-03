package event

import (
	"fmt"
	"strings"
)

func validateAppendRequest(request AppendRequest) error {
	if strings.TrimSpace(request.UserID) == "" {
		return fmt.Errorf("%w: user_id is required", ErrInvalidRequest)
	}
	if len(request.Events) == 0 && len(request.Commands) == 0 && request.JobFence == nil {
		return fmt.Errorf("%w: at least one event, command, or job fence is required", ErrInvalidRequest)
	}

	switch request.Actor.Kind {
	case ActorUser:
		if request.Idempotency == nil {
			return fmt.Errorf("%w: user actor requires idempotency", ErrInvalidRequest)
		}
		if request.CommandID != "" || request.JobFence != nil {
			return fmt.Errorf("%w: user actor cannot provide command_id or job fence", ErrInvalidRequest)
		}
	case ActorWorker:
		if request.JobFence == nil {
			return fmt.Errorf("%w: worker actor requires job fence", ErrInvalidRequest)
		}
		if request.Idempotency != nil || request.CommandID != "" {
			return fmt.Errorf("%w: worker actor cannot provide idempotency or command_id", ErrInvalidRequest)
		}
	case ActorSystem:
		if strings.TrimSpace(request.CommandID) == "" {
			return fmt.Errorf("%w: system actor requires command_id", ErrInvalidRequest)
		}
		if request.Idempotency != nil || request.JobFence != nil {
			return fmt.Errorf("%w: system actor cannot provide idempotency or job fence", ErrInvalidRequest)
		}
	default:
		return fmt.Errorf("%w: actor kind is required", ErrInvalidRequest)
	}

	if request.Idempotency != nil {
		if strings.TrimSpace(request.Idempotency.Scope) == "" {
			return fmt.Errorf("%w: idempotency scope is required", ErrInvalidRequest)
		}
		if strings.TrimSpace(request.Idempotency.Key) == "" {
			return fmt.Errorf("%w: idempotency key is required", ErrInvalidRequest)
		}
		if strings.TrimSpace(request.Idempotency.RequestHash) == "" {
			return fmt.Errorf("%w: idempotency request_hash is required", ErrInvalidRequest)
		}
	}
	if request.JobFence != nil {
		if strings.TrimSpace(request.JobFence.JobID) == "" || strings.TrimSpace(request.JobFence.LeaseToken) == "" {
			return fmt.Errorf("%w: job fence job_id and lease_token are required", ErrInvalidRequest)
		}
	}
	if request.Aggregate != nil {
		if strings.TrimSpace(request.Aggregate.RunID) == "" {
			return fmt.Errorf("%w: aggregate run_id is required", ErrInvalidRequest)
		}
		if request.Aggregate.ExpectedVersion < 0 {
			return fmt.Errorf("%w: aggregate expected version cannot be negative", ErrInvalidRequest)
		}
	}
	for _, event := range request.Events {
		if strings.TrimSpace(event.Type) == "" {
			return fmt.Errorf("%w: event type is required", ErrInvalidRequest)
		}
		if event.SchemaVersion <= 0 {
			return fmt.Errorf("%w: event schema_version must be positive", ErrInvalidRequest)
		}
	}
	for _, command := range request.Commands {
		if strings.TrimSpace(command.Kind) == "" {
			return fmt.Errorf("%w: command kind is required", ErrInvalidRequest)
		}
	}
	return nil
}
