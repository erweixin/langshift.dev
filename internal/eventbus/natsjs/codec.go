// Package natsjs provides the production JetStream transport for durable
// commands. PostgreSQL outbox/inbox rows remain the delivery authority.
package natsjs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
)

const envelopeVersion = 1

var (
	ErrConfiguration = errors.New("jetstream transport configuration is invalid")
	ErrEnvelope      = errors.New("jetstream command envelope is invalid")
)

var commandSubjects = map[string]string{
	"CancelRemainingChildRuns":           "lites.commands.queued.cancel-remaining-child-runs",
	"DeliverReminder":                    "lites.commands.product.deliver-reminder",
	"ExecuteToolCall":                    "lites.commands.queued.execute-tool-call",
	"GenerateDailyTask":                  "lites.commands.product.generate-daily-task",
	"GenerateMissionRoute":               "lites.commands.product.generate-mission-route",
	"NotifyApproval":                     "lites.commands.queued.notify-approval",
	"PrepareToolPreview":                 "lites.commands.queued.prepare-tool-preview",
	"PropagateRunCancellation":           "lites.commands.queued.propagate-run-cancellation",
	"ReconcileRunCancellation":           "lites.commands.queued.reconcile-run-cancellation",
	"ReconcileToolEffect":                "lites.commands.queued.reconcile-tool-effect",
	"ResumeAgentRun":                     "lites.commands.queued.resume-agent-run",
	"ResumeParentRun":                    "lites.commands.queued.resume-parent-run",
	"RoutePlanningRequested":             "lites.commands.product.route-planning-requested",
	"StartAgentRun":                      "lites.commands.queued.start-agent-run",
	"account.erasure.schedule":           "lites.commands.account.erasure.schedule",
	"account.export.prepare":             "lites.commands.account.export.prepare",
	"events.publish":                     "lites.commands.events.publish",
	"identity.email.change.verify":       "lites.commands.identity.email.change.verify",
	"identity.email.invitation":          "lites.commands.identity.email.invitation",
	"identity.email.password_reset":      "lites.commands.identity.email.password-reset",
	"identity.email.security":            "lites.commands.identity.email.security",
	"identity.email.verify":              "lites.commands.identity.email.verify",
	"identity.anonymous_claim.reconcile": "lites.commands.identity.anonymous-claim.reconcile",
	"identity.invitation_import.process": "lites.commands.identity.invitation-import.process",
	"identity.membership_import.process": "lites.commands.identity.membership-import.process",
}

var scheduledCommandSlugs = map[string]string{
	"CancelRemainingChildRuns": "cancel-remaining-child-runs",
	"ExecuteToolCall":          "execute-tool-call",
	"NotifyApproval":           "notify-approval",
	"PrepareToolPreview":       "prepare-tool-preview",
	"PropagateRunCancellation": "propagate-run-cancellation",
	"ReconcileRunCancellation": "reconcile-run-cancellation",
	"ReconcileToolEffect":      "reconcile-tool-effect",
	"ResumeAgentRun":           "resume-agent-run",
	"ResumeParentRun":          "resume-parent-run",
	"StartAgentRun":            "start-agent-run",
}

type commandEnvelope struct {
	Version       int    `json:"version"`
	TenantID      string `json:"tenant_id"`
	StoreEpoch    string `json:"store_epoch"`
	CommandID     string `json:"command_id"`
	CommandType   string `json:"command_type"`
	AggregateKind string `json:"aggregate_kind"`
	AggregateID   string `json:"aggregate_id"`
	PayloadRef    string `json:"payload_ref"`
	PayloadHash   string `json:"payload_hash"`
}

type dispatchEnvelope struct {
	commandEnvelope
	QueueGeneration uint64 `json:"queue_generation"`
	DispatchVersion uint64 `json:"dispatch_version"`
}

func SubjectFor(commandType string) (string, error) {
	subject, ok := commandSubjects[commandType]
	if !ok {
		return "", fmt.Errorf("%w: command type", ErrEnvelope)
	}
	return subject, nil
}

func DispatchSubjectFor(commandType string) (string, error) {
	slug, ok := scheduledCommandSlugs[commandType]
	if !ok {
		return "", fmt.Errorf("%w: unscheduled command type", ErrEnvelope)
	}
	return "lites.commands.dispatch." + slug, nil
}

func Encode(command eventpostgres.PublishedCommand) ([]byte, string, error) {
	if !validFields(command.TenantID, command.StoreEpoch, command.CommandID, command.CommandType, command.AggregateKind, command.AggregateID, command.PayloadRef, command.PayloadHash) {
		return nil, "", ErrEnvelope
	}
	subject, err := SubjectFor(command.CommandType)
	if err != nil {
		return nil, "", err
	}
	body, err := json.Marshal(commandEnvelope{Version: envelopeVersion, TenantID: command.TenantID, StoreEpoch: command.StoreEpoch, CommandID: command.CommandID, CommandType: command.CommandType, AggregateKind: command.AggregateKind, AggregateID: command.AggregateID, PayloadRef: command.PayloadRef, PayloadHash: command.PayloadHash})
	if err != nil {
		return nil, "", err
	}
	return body, subject, nil
}

func Decode(body []byte, subject string) (eventpostgres.DeliveredCommand, error) {
	if len(body) == 0 || len(body) > 64<<10 {
		return eventpostgres.DeliveredCommand{}, ErrEnvelope
	}
	var envelope commandEnvelope
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return eventpostgres.DeliveredCommand{}, ErrEnvelope
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return eventpostgres.DeliveredCommand{}, ErrEnvelope
	}
	expectedSubject, err := SubjectFor(envelope.CommandType)
	if err != nil || expectedSubject != subject || envelope.Version != envelopeVersion || !validFields(envelope.TenantID, envelope.StoreEpoch, envelope.CommandID, envelope.CommandType, envelope.AggregateKind, envelope.AggregateID, envelope.PayloadRef, envelope.PayloadHash) {
		return eventpostgres.DeliveredCommand{}, ErrEnvelope
	}
	return eventpostgres.DeliveredCommand{TenantID: envelope.TenantID, StoreEpoch: envelope.StoreEpoch, CommandID: envelope.CommandID, CommandType: envelope.CommandType, AggregateKind: envelope.AggregateKind, AggregateID: envelope.AggregateID, PayloadRef: envelope.PayloadRef, PayloadHash: envelope.PayloadHash}, nil
}

// EncodeDispatch creates the worker-visible envelope. Unlike the staged
// outbox envelope, it is inseparably bound to the current redelivery and
// scheduler lease generations.
func EncodeDispatch(command eventpostgres.PublishedCommand) ([]byte, string, error) {
	if command.QueueGeneration < 1 || command.DispatchVersion < 1 || !validFields(command.TenantID, command.StoreEpoch, command.CommandID, command.CommandType, command.AggregateKind, command.AggregateID, command.PayloadRef, command.PayloadHash) {
		return nil, "", ErrEnvelope
	}
	subject, err := DispatchSubjectFor(command.CommandType)
	if err != nil {
		return nil, "", err
	}
	body, err := json.Marshal(dispatchEnvelope{commandEnvelope: commandEnvelope{Version: envelopeVersion, TenantID: command.TenantID, StoreEpoch: command.StoreEpoch, CommandID: command.CommandID, CommandType: command.CommandType, AggregateKind: command.AggregateKind, AggregateID: command.AggregateID, PayloadRef: command.PayloadRef, PayloadHash: command.PayloadHash}, QueueGeneration: command.QueueGeneration, DispatchVersion: command.DispatchVersion})
	return body, subject, err
}

func DecodeDispatch(body []byte, subject string) (eventpostgres.DeliveredCommand, error) {
	if len(body) == 0 || len(body) > 64<<10 {
		return eventpostgres.DeliveredCommand{}, ErrEnvelope
	}
	var envelope dispatchEnvelope
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return eventpostgres.DeliveredCommand{}, ErrEnvelope
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return eventpostgres.DeliveredCommand{}, ErrEnvelope
	}
	expected, err := DispatchSubjectFor(envelope.CommandType)
	if err != nil || expected != subject || envelope.Version != envelopeVersion || envelope.QueueGeneration < 1 || envelope.DispatchVersion < 1 || !validFields(envelope.TenantID, envelope.StoreEpoch, envelope.CommandID, envelope.CommandType, envelope.AggregateKind, envelope.AggregateID, envelope.PayloadRef, envelope.PayloadHash) {
		return eventpostgres.DeliveredCommand{}, ErrEnvelope
	}
	return eventpostgres.DeliveredCommand{TenantID: envelope.TenantID, StoreEpoch: envelope.StoreEpoch, CommandID: envelope.CommandID, CommandType: envelope.CommandType, AggregateKind: envelope.AggregateKind, AggregateID: envelope.AggregateID, PayloadRef: envelope.PayloadRef, PayloadHash: envelope.PayloadHash, QueueGeneration: envelope.QueueGeneration, DispatchVersion: envelope.DispatchVersion}, nil
}

func validFields(fields ...string) bool {
	for _, field := range fields {
		if strings.TrimSpace(field) == "" || len(field) > 2048 || strings.ContainsAny(field, "\x00\r\n") {
			return false
		}
	}
	return true
}
