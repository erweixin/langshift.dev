package natsjs

import (
	"context"
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
