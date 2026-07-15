package natsjs

import (
	"context"
	"fmt"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/nats-io/nats.go/jetstream"
)

type publishAPI interface {
	Publish(context.Context, string, []byte, ...jetstream.PublishOpt) (*jetstream.PubAck, error)
}

// Broker implements the eventstore CommandBroker contract. A successful call
// means the JetStream leader acknowledged durable storage; only then may the
// caller mark the PostgreSQL outbox row published.
type Broker struct {
	Publisher     publishAPI
	Stream        string
	RetryWait     time.Duration
	RetryAttempts int
}

// DispatchBroker publishes only scheduler-admitted execution commands. Its
// message identity includes both fences, so a legitimate redelivery is not
// collapsed by JetStream's duplicate window.
type DispatchBroker Broker

func (broker Broker) Publish(ctx context.Context, command eventpostgres.PublishedCommand) error {
	if broker.Publisher == nil || broker.Stream == "" || broker.RetryWait <= 0 || broker.RetryAttempts < 0 {
		return ErrConfiguration
	}
	body, subject, err := Encode(command)
	if err != nil {
		return err
	}
	ack, err := broker.Publisher.Publish(ctx, subject, body,
		jetstream.WithMsgID(command.CommandID),
		jetstream.WithExpectStream(broker.Stream),
		jetstream.WithRetryWait(broker.RetryWait),
		jetstream.WithRetryAttempts(broker.RetryAttempts),
	)
	if err != nil {
		return err
	}
	if ack == nil || ack.Stream != broker.Stream {
		return ErrEnvelope
	}
	return nil
}

func (broker DispatchBroker) Publish(ctx context.Context, command eventpostgres.PublishedCommand) error {
	if broker.Publisher == nil || broker.Stream == "" || broker.RetryWait <= 0 || broker.RetryAttempts < 0 {
		return ErrConfiguration
	}
	body, subject, err := EncodeDispatch(command)
	if err != nil {
		return err
	}
	messageID := command.CommandID + ":" + fmt.Sprint(command.QueueGeneration) + ":" + fmt.Sprint(command.DispatchVersion)
	ack, err := broker.Publisher.Publish(ctx, subject, body, jetstream.WithMsgID(messageID), jetstream.WithExpectStream(broker.Stream), jetstream.WithRetryWait(broker.RetryWait), jetstream.WithRetryAttempts(broker.RetryAttempts))
	if err != nil {
		return err
	}
	if ack == nil || ack.Stream != broker.Stream {
		return ErrEnvelope
	}
	return nil
}
