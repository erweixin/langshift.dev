package postgres

import (
	"context"
	"errors"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/payload"
)

type AccountExportDispatcher struct {
	Store        AccountExportStore
	Payloads     payload.Store
	Inbox        eventpostgres.InboxStore
	StoreEpoch   string
	ConsumerName string
}

type AccountExportDispatchResult struct {
	Claimed, Completed, Replayed, TerminalFailure bool
	Export                                        AccountExportProcessResult
}

func (dispatcher AccountExportDispatcher) Dispatch(ctx context.Context, command eventpostgres.DeliveredCommand) (AccountExportDispatchResult, error) {
	if dispatcher.Payloads == nil || dispatcher.StoreEpoch == "" || dispatcher.StoreEpoch != command.StoreEpoch || command.CommandType != AccountExportPrepareCommand || command.AggregateKind != "data_export_request" || command.AggregateID == "" {
		return AccountExportDispatchResult{}, eventpostgres.ErrDeliveryConfiguration
	}
	consumer := dispatcher.ConsumerName
	if consumer == "" {
		consumer = defaultAccountErasureConsumer
	}
	claim, err := dispatcher.Inbox.Claim(ctx, consumer, command)
	if err != nil {
		return AccountExportDispatchResult{}, err
	}
	if claim.Completed {
		return AccountExportDispatchResult{Completed: true, Replayed: true}, nil
	}
	result := AccountExportDispatchResult{Claimed: true}
	dispatcher.Store.Inbox = dispatcher.Inbox
	body, err := dispatcher.Payloads.Get(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: command.CommandID, Class: "account-export-command", ContentType: "application/json"}, payload.Manifest{Ref: command.PayloadRef, Hash: command.PayloadHash})
	if err != nil {
		_ = dispatcher.Inbox.Abandon(ctx, claim)
		return result, err
	}
	work, err := DecodeAccountExportWork(body)
	if err != nil || work.RequestID != command.AggregateID {
		result.Export, err = dispatcher.Store.Fail(ctx, command, claim, accountExportFailureInvalidCommand)
		if err != nil {
			if abandonErr := dispatcher.Inbox.Abandon(ctx, claim); abandonErr != nil {
				return result, abandonErr
			}
			return result, err
		}
		result.Completed = result.Export.Completed
		result.Replayed = result.Export.Replayed
		result.TerminalFailure = result.Export.Status == "failed"
		return result, nil
	}
	result.Export, err = dispatcher.Store.Process(ctx, command, claim, work)
	if err != nil {
		if errors.Is(err, ErrAccountExportTooLarge) || errors.Is(err, ErrAccountExportInvalid) {
			reason := accountExportFailureInvalidCommand
			if errors.Is(err, ErrAccountExportTooLarge) {
				reason = accountExportFailureTooLarge
			}
			result.Export, err = dispatcher.Store.Fail(ctx, command, claim, reason)
			if err == nil {
				result.Completed = result.Export.Completed
				result.Replayed = result.Export.Replayed
				result.TerminalFailure = result.Export.Status == "failed"
				return result, nil
			}
		}
		if abandonErr := dispatcher.Inbox.Abandon(ctx, claim); abandonErr != nil {
			return result, abandonErr
		}
		return result, err
	}
	result.Completed = result.Export.Completed
	result.Replayed = result.Export.Replayed
	return result, nil
}
