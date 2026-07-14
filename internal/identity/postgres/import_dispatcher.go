package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/payload"
)

const defaultIdentityImportConsumer = "identity-import-worker"

type IdentityImportDispatcher struct {
	Service      AuthService
	Inbox        eventpostgres.InboxStore
	ConsumerName string
}

type ImportDispatchResult struct {
	Claimed, Completed, Replayed, TerminalFailure bool
	Import                                        ImportProcessResult
}

type importWorkCommand struct {
	ImportID    string `json:"import_id"`
	ObjectRef   string `json:"object_ref"`
	ContentHash string `json:"content_hash"`
	ImportKey   string `json:"import_key"`
	DefaultRole string `json:"default_role,omitempty"`
	Mode        string `json:"mode,omitempty"`
}

func (dispatcher IdentityImportDispatcher) Dispatch(ctx context.Context, command eventpostgres.DeliveredCommand) (ImportDispatchResult, error) {
	class, err := importCommandClass(command.CommandType)
	if err != nil || dispatcher.Service.Payloads == nil || !validImportAggregate(command) {
		return ImportDispatchResult{}, ErrImportInvalid
	}
	consumer := dispatcher.ConsumerName
	if consumer == "" {
		consumer = defaultIdentityImportConsumer
	}
	claim, err := dispatcher.Inbox.Claim(ctx, consumer, command)
	if errors.Is(err, eventpostgres.ErrDeliveryBusy) {
		return ImportDispatchResult{Claimed: false}, err
	}
	if err != nil {
		return ImportDispatchResult{}, err
	}
	if claim.Completed {
		return ImportDispatchResult{Completed: true, Replayed: true}, nil
	}
	result := ImportDispatchResult{Claimed: true}
	body, err := dispatcher.Service.Payloads.Get(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: command.CommandID, Class: class, ContentType: "application/json"}, payload.Manifest{Ref: command.PayloadRef, Hash: command.PayloadHash})
	if err != nil {
		_ = dispatcher.Inbox.Abandon(ctx, claim)
		return result, apiDependencyUnavailable()
	}
	work, err := decodeImportWorkCommand(body, command.CommandType)
	if err != nil || work.ImportID != command.AggregateID {
		if failErr := dispatcher.failDeliveredImport(ctx, command); failErr != nil {
			_ = dispatcher.Inbox.Abandon(ctx, claim)
			return result, failErr
		}
		if completeErr := dispatcher.Inbox.Complete(ctx, claim); completeErr != nil {
			return result, completeErr
		}
		result.Completed = true
		result.TerminalFailure = true
		return result, nil
	}
	switch command.CommandType {
	case "identity.invitation_import.process":
		result.Import, err = dispatcher.Service.ProcessInvitationImportDelivery(ctx, dispatcher.Inbox, claim, work.ImportID)
	case "identity.membership_import.process":
		result.Import, err = dispatcher.Service.ProcessMembershipImportDelivery(ctx, dispatcher.Inbox, claim, work.ImportID)
	}
	if err == nil {
		result.Completed = true
		return result, nil
	}
	if errors.Is(err, ErrImportInvalid) || errors.Is(err, ErrImportFailed) {
		// Permanent source/contract failures are terminal business outcomes. The
		// import row and security event were already committed by the processor.
		// Completing the inbox prevents a poison command from retrying forever.
		if completeErr := dispatcher.Inbox.Complete(ctx, claim); completeErr != nil && !errors.Is(completeErr, eventpostgres.ErrDeliveryConflict) {
			return result, completeErr
		}
		result.Completed = true
		result.TerminalFailure = true
		return result, nil
	}
	if abandonErr := dispatcher.Inbox.Abandon(ctx, claim); abandonErr != nil && !errors.Is(abandonErr, eventpostgres.ErrDeliveryConflict) {
		return result, abandonErr
	}
	return result, err
}

func validImportAggregate(command eventpostgres.DeliveredCommand) bool {
	switch command.CommandType {
	case "identity.invitation_import.process":
		return command.AggregateKind == "invitation_import"
	case "identity.membership_import.process":
		return command.AggregateKind == "membership_import"
	default:
		return false
	}
}

func (dispatcher IdentityImportDispatcher) failDeliveredImport(ctx context.Context, command eventpostgres.DeliveredCommand) error {
	switch command.CommandType {
	case "identity.invitation_import.process":
		record, terminal, err := dispatcher.Service.claimInvitationImport(ctx, command.TenantID, command.AggregateID)
		if terminal && (err == nil || errors.Is(err, ErrImportFailed)) {
			return nil
		}
		if err != nil {
			return err
		}
		return dispatcher.Service.failInvitationImport(ctx, record, "work_command_invalid")
	case "identity.membership_import.process":
		record, terminal, err := dispatcher.Service.claimMembershipImport(ctx, command.TenantID, command.AggregateID)
		if terminal && (err == nil || errors.Is(err, ErrImportFailed)) {
			return nil
		}
		if err != nil {
			return err
		}
		return dispatcher.Service.failMembershipImport(ctx, record, "work_command_invalid")
	default:
		return ErrImportInvalid
	}
}

func importCommandClass(commandType string) (string, error) {
	switch commandType {
	case "identity.invitation_import.process":
		return "invitation-import-command", nil
	case "identity.membership_import.process":
		return "membership-import-command", nil
	default:
		return "", ErrImportInvalid
	}
}

func decodeImportWorkCommand(body []byte, commandType string) (importWorkCommand, error) {
	var command importWorkCommand
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&command); err != nil {
		return importWorkCommand{}, ErrImportInvalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return importWorkCommand{}, ErrImportInvalid
	}
	if command.ImportID == "" || !validBoundImportMetadata(command.ObjectRef, command.ContentHash, command.ImportKey) {
		return importWorkCommand{}, ErrImportInvalid
	}
	switch commandType {
	case "identity.invitation_import.process":
		if !validInvitationRole(command.DefaultRole) || command.Mode != "" {
			return importWorkCommand{}, ErrImportInvalid
		}
	case "identity.membership_import.process":
		if command.DefaultRole != "" || (command.Mode != "upsert" && command.Mode != "deactivate_missing") {
			return importWorkCommand{}, ErrImportInvalid
		}
	default:
		return importWorkCommand{}, ErrImportInvalid
	}
	return command, nil
}
