package natsjs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
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

func TestJetStreamAgentContinuationCrashRedelivery100(t *testing.T) {
	url := os.Getenv("NATS_TEST_URL")
	if url == "" {
		t.Skip("NATS_TEST_URL is not set")
	}
	nc, err := nats.Connect(url, nats.Name("lites-agent-continuation-integration-test"), nats.Timeout(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	suffix := time.Now().UnixNano()
	streamName := fmt.Sprintf("LITES_AGENT_CONTINUATION_%d", suffix)
	consumerName := fmt.Sprintf("AGENT_WORKER_%d", suffix)
	defer func() { _ = js.DeleteStream(context.Background(), streamName) }()
	startSubject, _ := DispatchSubjectFor("StartAgentRun")
	resumeSubject, _ := DispatchSubjectFor("ResumeAgentRun")
	source, err := (Topology{
		Stream: streamName, Consumer: consumerName, FilterSubjects: []string{startSubject, resumeSubject}, Replicas: 1,
		MaxAge: time.Hour, MaxBytes: 32 << 20, DuplicateWindow: 2 * time.Minute,
		AckWait: time.Second, Backoff: []time.Duration{10 * time.Millisecond, 25 * time.Millisecond, 50 * time.Millisecond},
		MaxDeliver: 4, MaxAckPending: 256, MaxRequestBatch: 64, MaxRequestMaxBytes: 1 << 20,
	}).Provision(ctx, js)
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	attempts := make(map[string]int, 200)
	successes := make(map[string]int, 200)
	completed := make(chan eventpostgres.DeliveredCommand, 200)
	handler := func(_ context.Context, command eventpostgres.DeliveredCommand) error {
		mu.Lock()
		attempts[command.CommandID]++
		attempt := attempts[command.CommandID]
		mu.Unlock()
		if attempt == 1 {
			return errors.New("injected worker crash before ack")
		}
		if attempt > 2 {
			return eventpostgres.ErrDeliveryConflict
		}
		mu.Lock()
		successes[command.CommandID]++
		mu.Unlock()
		completed <- command
		return nil
	}
	consumerCtx, stopConsumer := context.WithCancel(ctx)
	consumerDone := make(chan error, 1)
	go func() {
		consumerDone <- (Consumer{Source: source, Handle: handler, Dispatch: true, Concurrency: 64, PullExpires: time.Second, HeartbeatInterval: 100 * time.Millisecond, BusyDelay: 10 * time.Millisecond, RetryDelay: 10 * time.Millisecond, AckTimeout: 2 * time.Second}).Run(consumerCtx)
	}()
	broker := DispatchBroker{Publisher: js, Stream: streamName, RetryWait: 10 * time.Millisecond, RetryAttempts: 2}
	publishPhase := func(commandType string, generation uint64) {
		t.Helper()
		for repetition := 0; repetition < 100; repetition++ {
			prefix := "1"
			if commandType == "ResumeAgentRun" {
				prefix = "2"
			}
			command := eventpostgres.PublishedCommand{
				TenantID: "10000000-0000-4000-8000-000000000001", StoreEpoch: "30000000-0000-4000-8000-000000000001",
				CommandID: fmt.Sprintf("%s0000000-0000-4000-8000-%012d", prefix, repetition+1), CommandType: commandType,
				AggregateKind: "run", AggregateID: fmt.Sprintf("40000000-0000-4000-8000-%012d", repetition+1),
				PayloadRef: fmt.Sprintf("encrypted://agent/%s/%d", commandType, repetition), PayloadHash: fmt.Sprintf("agent-%s-%d", commandType, repetition),
				QueueGeneration: generation, DispatchVersion: generation,
			}
			if publishErr := broker.Publish(ctx, command); publishErr != nil {
				t.Fatal(publishErr)
			}
		}
		for received := 0; received < 100; received++ {
			select {
			case command := <-completed:
				if command.CommandType != commandType || command.QueueGeneration != generation || command.DispatchVersion != generation {
					t.Fatalf("phase %s received %#v", commandType, command)
				}
			case runErr := <-consumerDone:
				t.Fatalf("phase %s consumer exited early: %v", commandType, runErr)
			case <-ctx.Done():
				mu.Lock()
				once, twice, extra := 0, 0, 0
				for _, count := range attempts {
					switch count {
					case 1:
						once++
					case 2:
						twice++
					default:
						extra++
					}
				}
				seen := len(attempts)
				mu.Unlock()
				select {
				case runErr := <-consumerDone:
					t.Fatalf("phase %s consumer exited: seen=%d once=%d twice=%d extra=%d err=%v", commandType, seen, once, twice, extra, runErr)
				default:
					t.Fatalf("phase %s timed out: seen=%d once=%d twice=%d extra=%d err=%v", commandType, seen, once, twice, extra, ctx.Err())
				}
			}
		}
	}
	publishPhase("StartAgentRun", 1)
	publishPhase("ResumeAgentRun", 2)

	deadline := time.Now().Add(5 * time.Second)
	for {
		info, infoErr := source.Info(ctx)
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		if info.NumAckPending == 0 && info.NumPending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("consumer did not drain: pending=%d ack_pending=%d", info.NumPending, info.NumAckPending)
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	if len(attempts) != 200 {
		t.Fatalf("unique commands=%d", len(attempts))
	}
	transportDuplicates := 0
	for commandID, count := range attempts {
		if count < 2 {
			t.Fatalf("command %s deliveries=%d", commandID, count)
		}
		if successes[commandID] != 1 {
			t.Fatalf("command %s successful executions=%d", commandID, successes[commandID])
		}
		transportDuplicates += count - 2
	}
	mu.Unlock()
	stopConsumer()
	if runErr := <-consumerDone; runErr != nil {
		t.Fatal(runErr)
	}
	t.Logf(`agent_continuation={"repetitions":100,"start_commands":100,"resume_commands":100,"injected_crashes":200,"successful_redeliveries":200,"transport_duplicates":%d,"duplicate_successes":0}`, transportDuplicates)
}
