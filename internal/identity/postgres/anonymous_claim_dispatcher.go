package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/identity/anonymousclaim"
	"github.com/langshift/lites/internal/payload"
)

const (
	AnonymousClaimReconcileCommand = "identity.anonymous_claim.reconcile"
	defaultAnonymousClaimConsumer  = "anonymous-claim-worker"
)

type ClaimReconciler interface {
	Reconcile(context.Context, string) (anonymousclaim.Saga, error)
}

type AnonymousClaimDispatcher struct {
	Reconciler   ClaimReconciler
	Payloads     payload.Store
	Inbox        eventpostgres.InboxStore
	StoreEpoch   string
	ConsumerName string
}

type AnonymousClaimDispatchResult struct {
	Claimed, Completed, Replayed, TerminalFailure bool
	Saga                                          anonymousclaim.Saga
}

func (dispatcher AnonymousClaimDispatcher) Dispatch(ctx context.Context, command eventpostgres.DeliveredCommand) (AnonymousClaimDispatchResult, error) {
	if dispatcher.Reconciler == nil || dispatcher.Payloads == nil || dispatcher.StoreEpoch == "" || command.StoreEpoch != dispatcher.StoreEpoch || command.CommandType != AnonymousClaimReconcileCommand || command.AggregateKind != "anonymous_claim" || command.AggregateID == "" {
		return AnonymousClaimDispatchResult{}, eventpostgres.ErrDeliveryConfiguration
	}
	consumer := dispatcher.ConsumerName
	if consumer == "" {
		consumer = defaultAnonymousClaimConsumer
	}
	claim, err := dispatcher.Inbox.Claim(ctx, consumer, command)
	if err != nil {
		return AnonymousClaimDispatchResult{}, err
	}
	if claim.Completed {
		return AnonymousClaimDispatchResult{Completed: true, Replayed: true}, nil
	}
	result := AnonymousClaimDispatchResult{Claimed: true}
	body, err := dispatcher.Payloads.Get(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: command.CommandID, Class: "anonymous-claim-command", ContentType: "application/json"}, payload.Manifest{Ref: command.PayloadRef, Hash: command.PayloadHash})
	if err != nil {
		_ = dispatcher.Inbox.Abandon(ctx, claim)
		return result, err
	}
	claimID, err := decodeClaimCommand(body)
	if err != nil || claimID != command.AggregateID {
		if completeErr := dispatcher.Inbox.Complete(ctx, claim); completeErr != nil {
			return result, completeErr
		}
		result.Completed = true
		result.TerminalFailure = true
		return result, nil
	}
	result.Saga, err = dispatcher.Reconciler.Reconcile(ctx, claimID)
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

func decodeClaimCommand(body []byte) (string, error) {
	var command struct {
		ClaimID string `json:"claim_id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&command); err != nil {
		return "", err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || command.ClaimID == "" {
		return "", anonymousclaim.ErrInvariant
	}
	return command.ClaimID, nil
}
