package mail

import (
	"context"
	"errors"
	"net/url"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/payload"
)

const DefaultConsumerName = "identity-import-worker"

type Dispatcher struct {
	Payloads     payload.Store
	Inbox        eventpostgres.InboxStore
	Sender       Sender
	AppURL       *url.URL
	StoreEpoch   string
	ConsumerName string
	Now          func() time.Time
}

type DispatchResult struct {
	Claimed, Completed, Replayed, TerminalFailure bool
}

func (dispatcher Dispatcher) Dispatch(ctx context.Context, command eventpostgres.DeliveredCommand) (DispatchResult, error) {
	if dispatcher.Payloads == nil || dispatcher.Sender == nil || dispatcher.AppURL == nil || dispatcher.StoreEpoch == "" || command.StoreEpoch != dispatcher.StoreEpoch || !IsCommandType(command.CommandType) {
		return DispatchResult{}, eventpostgres.ErrDeliveryConfiguration
	}
	consumer := dispatcher.ConsumerName
	if consumer == "" {
		consumer = DefaultConsumerName
	}
	claim, err := dispatcher.Inbox.Claim(ctx, consumer, command)
	if err != nil {
		return DispatchResult{}, err
	}
	if claim.Completed {
		return DispatchResult{Completed: true, Replayed: true}, nil
	}
	result := DispatchResult{Claimed: true}
	body, err := dispatcher.Payloads.Get(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: command.CommandID, Class: "mail-command", ContentType: "application/json"}, payload.Manifest{Ref: command.PayloadRef, Hash: command.PayloadHash})
	if err != nil {
		_ = dispatcher.Inbox.Abandon(ctx, claim)
		return result, err
	}
	mailCommand, err := Decode(body)
	if err == nil && !mailCommand.ExpiresAt.IsZero() && !mailCommand.ExpiresAt.After(dispatcher.now()) {
		err = ErrInvalidCommand
	}
	if err != nil {
		if completeErr := dispatcher.Inbox.Complete(ctx, claim); completeErr != nil {
			return result, completeErr
		}
		result.Completed = true
		result.TerminalFailure = true
		return result, nil
	}
	message, err := Render(mailCommand, dispatcher.AppURL)
	if err == nil {
		err = dispatcher.Sender.Send(ctx, command.CommandID, message)
	}
	if err != nil {
		if abandonErr := dispatcher.Inbox.Abandon(ctx, claim); abandonErr != nil && !errors.Is(abandonErr, eventpostgres.ErrDeliveryConflict) {
			return result, abandonErr
		}
		return result, err
	}
	// SMTP is at-least-once. A crash after the server accepts DATA can replay,
	// so every retry carries the same RFC Message-ID derived from command_id.
	if err = dispatcher.Inbox.Complete(ctx, claim); err != nil {
		return result, err
	}
	result.Completed = true
	return result, nil
}

func (dispatcher Dispatcher) now() time.Time {
	if dispatcher.Now != nil {
		return dispatcher.Now().UTC()
	}
	return time.Now().UTC()
}

func IsCommandType(commandType string) bool {
	switch commandType {
	case "identity.email.verify", "identity.email.password_reset", "identity.email.change.verify", "identity.email.invitation", "identity.email.security":
		return true
	default:
		return false
	}
}
