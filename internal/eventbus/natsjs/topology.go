package natsjs

import (
	"context"
	"sort"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

type Topology struct {
	Stream             string
	Consumer           string
	FilterSubjects     []string
	Replicas           int
	MaxAge             time.Duration
	MaxBytes           int64
	DuplicateWindow    time.Duration
	AckWait            time.Duration
	Backoff            []time.Duration
	MaxDeliver         int
	MaxAckPending      int
	MaxRequestBatch    int
	MaxRequestMaxBytes int
}

type CommandStream struct {
	Name            string
	Replicas        int
	MaxAge          time.Duration
	MaxBytes        int64
	DuplicateWindow time.Duration
}

type DurableConsumer struct {
	Stream             string
	Name               string
	FilterSubjects     []string
	Replicas           int
	AckWait            time.Duration
	Backoff            []time.Duration
	MaxDeliver         int
	MaxAckPending      int
	MaxRequestBatch    int
	MaxRequestMaxBytes int
}

func (consumer DurableConsumer) Provision(ctx context.Context, js jetstream.JetStream) (jetstream.Consumer, error) {
	if js == nil || consumer.Stream == "" || consumer.Name == "" || consumer.Replicas < 1 || consumer.AckWait <= 0 || consumer.MaxDeliver < 1 || len(consumer.Backoff) == 0 || len(consumer.Backoff) > consumer.MaxDeliver || consumer.MaxAckPending < 1 || consumer.MaxRequestBatch < 1 || consumer.MaxRequestMaxBytes < 1 || len(consumer.FilterSubjects) == 0 {
		return nil, ErrConfiguration
	}
	for _, subject := range consumer.FilterSubjects {
		known := false
		for _, allowed := range commandSubjects {
			if subject == allowed {
				known = true
				break
			}
		}
		if !known {
			return nil, ErrConfiguration
		}
	}
	return js.CreateOrUpdateConsumer(ctx, consumer.Stream, jetstream.ConsumerConfig{Name: consumer.Name, Durable: consumer.Name, Description: "Lites durable command worker", DeliverPolicy: jetstream.DeliverAllPolicy, AckPolicy: jetstream.AckExplicitPolicy, AckWait: consumer.AckWait, MaxDeliver: consumer.MaxDeliver, BackOff: append([]time.Duration(nil), consumer.Backoff...), FilterSubjects: append([]string(nil), consumer.FilterSubjects...), ReplayPolicy: jetstream.ReplayInstantPolicy, MaxAckPending: consumer.MaxAckPending, MaxRequestBatch: consumer.MaxRequestBatch, MaxRequestExpires: 30 * time.Second, MaxRequestMaxBytes: consumer.MaxRequestMaxBytes, Replicas: consumer.Replicas})
}

func (stream CommandStream) Provision(ctx context.Context, js jetstream.JetStream) error {
	if js == nil || stream.Name == "" || stream.Replicas < 1 || stream.MaxAge <= 0 || stream.MaxBytes <= 0 || stream.DuplicateWindow <= 0 {
		return ErrConfiguration
	}
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{Name: stream.Name, Description: "Lites durable commands", Subjects: allSubjects(), Retention: jetstream.LimitsPolicy, MaxBytes: stream.MaxBytes, Discard: jetstream.DiscardOld, MaxAge: stream.MaxAge, MaxMsgSize: 64 << 10, Storage: jetstream.FileStorage, Replicas: stream.Replicas, Duplicates: stream.DuplicateWindow, DenyDelete: true, DenyPurge: true})
	return err
}

// Provision creates or reconciles the durable file-backed stream and pull
// consumer. Production callers must use at least three replicas.
func (topology Topology) Provision(ctx context.Context, js jetstream.JetStream) (jetstream.Consumer, error) {
	if js == nil || topology.Stream == "" || topology.Consumer == "" || topology.Replicas < 1 || topology.MaxAge <= 0 || topology.MaxBytes <= 0 || topology.DuplicateWindow <= 0 || topology.AckWait <= 0 || topology.MaxDeliver < 1 || len(topology.Backoff) == 0 || len(topology.Backoff) > topology.MaxDeliver || topology.MaxAckPending < 1 || topology.MaxRequestBatch < 1 || topology.MaxRequestMaxBytes < 1 || len(topology.FilterSubjects) == 0 {
		return nil, ErrConfiguration
	}
	if err := (CommandStream{Name: topology.Stream, Replicas: topology.Replicas, MaxAge: topology.MaxAge, MaxBytes: topology.MaxBytes, DuplicateWindow: topology.DuplicateWindow}).Provision(ctx, js); err != nil {
		return nil, err
	}
	return (DurableConsumer{Stream: topology.Stream, Name: topology.Consumer, FilterSubjects: topology.FilterSubjects, Replicas: topology.Replicas, AckWait: topology.AckWait, Backoff: topology.Backoff, MaxDeliver: topology.MaxDeliver, MaxAckPending: topology.MaxAckPending, MaxRequestBatch: topology.MaxRequestBatch, MaxRequestMaxBytes: topology.MaxRequestMaxBytes}).Provision(ctx, js)
}

func allSubjects() []string {
	subjects := make([]string, 0, len(commandSubjects))
	for _, subject := range commandSubjects {
		subjects = append(subjects, subject)
	}
	sort.Strings(subjects)
	return subjects
}
