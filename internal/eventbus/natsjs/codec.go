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

func SubjectFor(commandType string) (string, error) {
	subject, ok := commandSubjects[commandType]
	if !ok {
		return "", fmt.Errorf("%w: command type", ErrEnvelope)
	}
	return subject, nil
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

func validFields(fields ...string) bool {
	for _, field := range fields {
		if strings.TrimSpace(field) == "" || len(field) > 2048 || strings.ContainsAny(field, "\x00\r\n") {
			return false
		}
	}
	return true
}
