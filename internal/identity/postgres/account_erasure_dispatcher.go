package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/identity/erasure"
	"github.com/langshift/lites/internal/payload"
)

const (
	AccountErasureScheduleCommand = "account.erasure.schedule"
	defaultAccountErasureConsumer = "account-erasure-worker"
)

type AccountErasureDispatcher struct {
	Service      erasure.Service
	Payloads     payload.Store
	Inbox        eventpostgres.InboxStore
	StoreEpoch   string
	ConsumerName string
}

type AccountErasureDispatchResult struct {
	Claimed, Completed, Replayed, TerminalFailure bool
	Erasure                                       erasure.Result
}

func (dispatcher AccountErasureDispatcher) Dispatch(ctx context.Context, command eventpostgres.DeliveredCommand) (AccountErasureDispatchResult, error) {
	if dispatcher.Payloads == nil || dispatcher.StoreEpoch == "" || dispatcher.StoreEpoch != command.StoreEpoch || command.CommandType != AccountErasureScheduleCommand || command.AggregateKind != "account_erasure_request" || command.AggregateID == "" {
		return AccountErasureDispatchResult{}, eventpostgres.ErrDeliveryConfiguration
	}
	consumer := dispatcher.ConsumerName
	if consumer == "" {
		consumer = defaultAccountErasureConsumer
	}
	claim, err := dispatcher.Inbox.Claim(ctx, consumer, command)
	if err != nil {
		return AccountErasureDispatchResult{}, err
	}
	if claim.Completed {
		return AccountErasureDispatchResult{Completed: true, Replayed: true}, nil
	}
	result := AccountErasureDispatchResult{Claimed: true}
	body, err := dispatcher.Payloads.Get(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: command.CommandID, Class: "account-erasure-command", ContentType: "application/json"}, payload.Manifest{Ref: command.PayloadRef, Hash: command.PayloadHash})
	if err != nil {
		// The payload surface deliberately removes this command object before
		// later surfaces run. A retry may continue without it only when the
		// durable receipt proves that the same request already completed that
		// surface in the current recovery epoch.
		resumable, receiptErr := canResumeErasureAfterPayloadPurge(ctx, dispatcher.Service.Store, command.TenantID, command.AggregateID, dispatcher.Service.RecoveryEpoch)
		if receiptErr != nil || !resumable {
			_ = dispatcher.Inbox.Abandon(ctx, claim)
			if receiptErr != nil {
				return result, receiptErr
			}
			return result, err
		}
	} else {
		work, decodeErr := decodeAccountErasureWork(body)
		if decodeErr != nil || work.RequestID != command.AggregateID {
			if completeErr := dispatcher.Inbox.Complete(ctx, claim); completeErr != nil {
				return result, completeErr
			}
			result.Completed = true
			result.TerminalFailure = true
			return result, nil
		}
	}
	result.Erasure, err = dispatcher.Service.Process(ctx, command.TenantID, command.AggregateID)
	if errors.Is(err, erasure.ErrCancelled) {
		if completeErr := dispatcher.Inbox.Complete(ctx, claim); completeErr != nil {
			return result, completeErr
		}
		result.Completed = true
		result.TerminalFailure = true
		return result, nil
	}
	if err != nil {
		if abandonErr := dispatcher.Inbox.Abandon(ctx, claim); abandonErr != nil && !errors.Is(abandonErr, eventpostgres.ErrDeliveryConflict) {
			return result, abandonErr
		}
		return result, err
	}
	if err = dispatcher.Inbox.Complete(ctx, claim); err != nil {
		return result, err
	}
	result.Completed = true
	return result, nil
}

func canResumeErasureAfterPayloadPurge(ctx context.Context, store erasure.Store, tenantID, requestID, recoveryEpoch string) (bool, error) {
	if store == nil || tenantID == "" || requestID == "" || recoveryEpoch == "" {
		return false, erasure.ErrInvalid
	}
	receipt, exists, err := store.LoadReceipt(ctx, tenantID, requestID, erasure.Payload, recoveryEpoch)
	if err != nil || !exists {
		return false, err
	}
	return receipt.TenantID == tenantID && receipt.RequestID == requestID && receipt.RecoveryEpoch == recoveryEpoch && receipt.Surface == erasure.Payload, nil
}

type accountErasureWork struct {
	RequestID    string    `json:"request_id"`
	UserID       string    `json:"user_id"`
	ScheduledFor time.Time `json:"scheduled_for"`
}

func decodeAccountErasureWork(body []byte) (accountErasureWork, error) {
	var work accountErasureWork
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&work); err != nil {
		return accountErasureWork{}, erasure.ErrInvalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || work.RequestID == "" || work.UserID == "" || work.ScheduledFor.IsZero() {
		return accountErasureWork{}, erasure.ErrInvalid
	}
	return work, nil
}
