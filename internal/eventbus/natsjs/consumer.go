package natsjs

import (
	"context"
	"errors"
	"sync"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/nats-io/nats.go/jetstream"
)

type Handler func(context.Context, eventpostgres.DeliveredCommand) error
type ErrorHandler func(context.Context, error)

type Consumer struct {
	Source            jetstream.Consumer
	Handle            Handler
	OnError           ErrorHandler
	Concurrency       int
	PullExpires       time.Duration
	HeartbeatInterval time.Duration
	BusyDelay         time.Duration
	RetryDelay        time.Duration
	AckTimeout        time.Duration
	// Dispatch selects the scheduler-admitted envelope decoder. Execution
	// workers must set it; direct infrastructure consumers leave it false.
	Dispatch bool
}

// Run processes a durable pull consumer until ctx is cancelled. Drain waits
// for buffered callbacks, while each callback is bounded by AckTimeout.
func (consumer Consumer) Run(ctx context.Context) error {
	if err := consumer.validate(); err != nil {
		return err
	}
	semaphore := make(chan struct{}, consumer.Concurrency)
	var handlers sync.WaitGroup
	consume, err := consumer.Source.Consume(func(message jetstream.Msg) {
		semaphore <- struct{}{}
		handlers.Add(1)
		defer func() { <-semaphore; handlers.Done() }()
		consumer.process(ctx, message)
	}, jetstream.PullExpiry(consumer.PullExpires), jetstream.PullMaxMessages(consumer.Concurrency), jetstream.ConsumeErrHandler(func(_ jetstream.ConsumeContext, consumeErr error) {
		consumer.report(ctx, consumeErr)
	}))
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		consume.Drain()
	case <-consume.Closed():
	}
	<-consume.Closed()
	handlers.Wait()
	return nil
}

func (consumer Consumer) process(parent context.Context, message jetstream.Msg) {
	var command eventpostgres.DeliveredCommand
	var err error
	if consumer.Dispatch {
		command, err = DecodeDispatch(message.Data(), message.Subject())
	} else {
		command, err = Decode(message.Data(), message.Subject())
	}
	if err != nil {
		consumer.report(parent, err)
		if termErr := message.TermWithReason("invalid_command_envelope"); termErr != nil {
			consumer.report(parent, termErr)
		}
		return
	}
	ctx, cancel := context.WithTimeout(parent, consumer.AckTimeout)
	defer cancel()
	done := make(chan struct{})
	go consumer.keepAlive(ctx, done, message)
	err = consumer.Handle(ctx, command)
	close(done)
	switch {
	case err == nil:
		if ackErr := message.DoubleAck(ctx); ackErr != nil {
			consumer.report(parent, ackErr)
		}
	case errors.Is(err, eventpostgres.ErrDeliveryBusy):
		consumer.report(parent, err)
		if nakErr := message.NakWithDelay(consumer.BusyDelay); nakErr != nil {
			consumer.report(parent, nakErr)
		}
	case errors.Is(err, eventpostgres.ErrStaleStoreEpoch), errors.Is(err, eventpostgres.ErrDeliveryConflict):
		consumer.report(parent, err)
		if termErr := message.TermWithReason("non_retryable_delivery_conflict"); termErr != nil {
			consumer.report(parent, termErr)
		}
	default:
		consumer.report(parent, err)
		if nakErr := message.NakWithDelay(consumer.RetryDelay); nakErr != nil {
			consumer.report(parent, nakErr)
		}
	}
}

func (consumer Consumer) keepAlive(ctx context.Context, done <-chan struct{}, message jetstream.Msg) {
	ticker := time.NewTicker(consumer.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := message.InProgress(); err != nil {
				consumer.report(ctx, err)
			}
		}
	}
}

func (consumer Consumer) validate() error {
	if consumer.Source == nil || consumer.Handle == nil || consumer.Concurrency < 1 || consumer.PullExpires <= 0 || consumer.HeartbeatInterval <= 0 || consumer.BusyDelay <= 0 || consumer.RetryDelay <= 0 || consumer.AckTimeout <= consumer.HeartbeatInterval {
		return ErrConfiguration
	}
	return nil
}

func (consumer Consumer) report(ctx context.Context, err error) {
	if err != nil && consumer.OnError != nil {
		consumer.OnError(ctx, err)
	}
}
