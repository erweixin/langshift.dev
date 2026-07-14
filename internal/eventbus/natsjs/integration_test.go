package natsjs

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func TestJetStreamDurablePublishDedupAndConsume(t *testing.T) {
	url := os.Getenv("NATS_TEST_URL")
	if url == "" {
		t.Skip("NATS_TEST_URL is not set")
	}
	nc, err := nats.Connect(url, nats.Name("lites-jetstream-integration-test"), nats.Timeout(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	suffix := time.Now().UnixNano()
	streamName := fmt.Sprintf("LITES_COMMANDS_%d", suffix)
	consumerName := fmt.Sprintf("IDENTITY_IMPORT_%d", suffix)
	defer func() { _ = js.DeleteStream(context.Background(), streamName) }()
	consumer, err := (Topology{Stream: streamName, Consumer: consumerName, FilterSubjects: []string{"lites.commands.identity.invitation-import.process"}, Replicas: 1, MaxAge: time.Hour, MaxBytes: 16 << 20, DuplicateWindow: 2 * time.Minute, AckWait: 2 * time.Second, Backoff: []time.Duration{100 * time.Millisecond, 250 * time.Millisecond, time.Second}, MaxDeliver: 3, MaxAckPending: 8, MaxRequestBatch: 8, MaxRequestMaxBytes: 512 << 10}).Provision(ctx, js)
	if err != nil {
		t.Fatal(err)
	}
	broker := Broker{Publisher: js, Stream: streamName, RetryWait: 50 * time.Millisecond, RetryAttempts: 2}
	command := testPublishedCommand()
	if err = broker.Publish(ctx, command); err != nil {
		t.Fatal(err)
	}
	if err = broker.Publish(ctx, command); err != nil {
		t.Fatal(err)
	}
	stream, err := js.Stream(ctx, streamName)
	if err != nil {
		t.Fatal(err)
	}
	info, err := stream.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.State.Msgs != 1 {
		t.Fatalf("expected server dedupe to retain one message, got %d", info.State.Msgs)
	}
	message, err := consumer.Next(jetstream.FetchMaxWait(3 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	delivered, err := Decode(message.Data(), message.Subject())
	if err != nil {
		t.Fatal(err)
	}
	if delivered != command.Delivered() {
		t.Fatalf("delivery identity changed: %#v", delivered)
	}
	if err = message.DoubleAck(ctx); err != nil {
		t.Fatal(err)
	}
	consumerInfo, err := consumer.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if consumerInfo.NumAckPending != 0 || consumerInfo.Config.AckPolicy != jetstream.AckExplicitPolicy || consumerInfo.Config.DeliverSubject != "" {
		t.Fatalf("consumer is not an acknowledged durable pull consumer: %#v", consumerInfo)
	}
}
