package realtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	realtimepostgres "github.com/langshift/lites/internal/realtime/postgres"
)

type fakeStore struct {
	mu     sync.Mutex
	events []realtimepostgres.Event
	order  *[]string
}

type blockingStore struct{}

type blockingControlSink struct{}

func (blockingControlSink) Event(context.Context, realtimepostgres.Event) error { return nil }
func (blockingControlSink) Control(ctx context.Context, _ Control) error {
	<-ctx.Done()
	return ctx.Err()
}

func (blockingStore) HighWatermark(ctx context.Context, _, _ string) (uint64, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}

func (blockingStore) List(context.Context, string, string, uint64, uint64, int) ([]realtimepostgres.Event, error) {
	return nil, nil
}

func (store *fakeStore) HighWatermark(context.Context, string, string) (uint64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.order != nil {
		*store.order = append(*store.order, "high-watermark")
	}
	if len(store.events) == 0 {
		return 0, nil
	}
	return store.events[len(store.events)-1].Sequence, nil
}

func (store *fakeStore) List(_ context.Context, _, _ string, after, through uint64, limit int) ([]realtimepostgres.Event, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make([]realtimepostgres.Event, 0, limit)
	for _, event := range store.events {
		if event.Sequence > after && event.Sequence <= through {
			result = append(result, event)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

func (store *fakeStore) append(events ...realtimepostgres.Event) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.events = append(store.events, events...)
}

type fakeSubscription struct{ notifications chan struct{} }

func (subscription *fakeSubscription) Notifications() <-chan struct{} {
	return subscription.notifications
}
func (subscription *fakeSubscription) Close() error { return nil }

type fakeWakes struct {
	subscription *fakeSubscription
	order        *[]string
}

func (wakes fakeWakes) Subscribe(context.Context, string, string) (WakeSubscription, error) {
	if wakes.order != nil {
		*wakes.order = append(*wakes.order, "subscribe")
	}
	return wakes.subscription, nil
}

type collectingSink struct {
	events   chan realtimepostgres.Event
	controls chan Control
	err      error
}

func (sink collectingSink) Event(context.Context, realtimepostgres.Event) error {
	if sink.err != nil {
		return sink.err
	}
	return nil
}

func (sink collectingSink) Control(context.Context, Control) error { return sink.err }

type channelSink struct {
	events   chan realtimepostgres.Event
	controls chan Control
}

func (sink channelSink) Event(ctx context.Context, event realtimepostgres.Event) error {
	select {
	case sink.events <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (sink channelSink) Control(ctx context.Context, control Control) error {
	select {
	case sink.controls <- control:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func productionTestStream(store EventStore, wakes WakeSource) Stream {
	return Stream{Store: store, Wakes: wakes, PageSize: 2, Heartbeat: time.Hour, CatchUpInterval: time.Hour, WakeRetry: time.Hour, SendTimeout: time.Second, ReauthLead: time.Minute}
}

func TestStreamSubscribesBeforeSnapshotAndDeliversExactlyOnceInOrder(t *testing.T) {
	order := []string{}
	store := &fakeStore{events: []realtimepostgres.Event{{Sequence: 1}, {Sequence: 2}, {Sequence: 3}}, order: &order}
	subscription := &fakeSubscription{notifications: make(chan struct{}, 4)}
	wakes := fakeWakes{subscription: subscription, order: &order}
	sink := channelSink{events: make(chan realtimepostgres.Event, 8), controls: make(chan Control, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- productionTestStream(store, wakes).Run(ctx, Session{TenantID: "tenant", UserID: "user", ExpiresAt: time.Now().Add(time.Hour)}, sink)
	}()

	for expected := uint64(1); expected <= 3; expected++ {
		select {
		case event := <-sink.events:
			if event.Sequence != expected {
				t.Fatalf("event sequence = %d, want %d", event.Sequence, expected)
			}
		case <-time.After(time.Second):
			t.Fatal("initial backfill timed out")
		}
	}
	ready := <-sink.controls
	if ready.Kind != "ready" || ready.Cursor != 3 {
		t.Fatalf("ready control = %#v", ready)
	}
	if len(order) < 2 || order[0] != "subscribe" || order[1] != "high-watermark" {
		t.Fatalf("protocol order = %v", order)
	}

	store.append(realtimepostgres.Event{Sequence: 4}, realtimepostgres.Event{Sequence: 5})
	// Duplicate and out-of-order bus wakes are only hints. Both catch-ups read
	// the authoritative cursor and therefore cannot duplicate an event.
	subscription.notifications <- struct{}{}
	subscription.notifications <- struct{}{}
	for expected := uint64(4); expected <= 5; expected++ {
		select {
		case event := <-sink.events:
			if event.Sequence != expected {
				t.Fatalf("live event sequence = %d, want %d", event.Sequence, expected)
			}
		case <-time.After(time.Second):
			t.Fatal("live catch-up timed out")
		}
	}
	select {
	case event := <-sink.events:
		t.Fatalf("duplicate event delivered: %#v", event)
	case <-time.After(25 * time.Millisecond):
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() = %v", err)
	}
}

func TestStreamPollsAfterWakeSourceDisconnects(t *testing.T) {
	store := &fakeStore{events: []realtimepostgres.Event{{Sequence: 1}}}
	subscription := &fakeSubscription{notifications: make(chan struct{})}
	sink := channelSink{events: make(chan realtimepostgres.Event, 4), controls: make(chan Control, 2)}
	stream := productionTestStream(store, fakeWakes{subscription: subscription})
	stream.CatchUpInterval = 10 * time.Millisecond
	stream.WakeRetry = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- stream.Run(ctx, Session{TenantID: "tenant", UserID: "user", ExpiresAt: time.Now().Add(time.Hour)}, sink)
	}()
	<-sink.events
	<-sink.controls
	close(subscription.notifications)
	store.append(realtimepostgres.Event{Sequence: 2})
	select {
	case event := <-sink.events:
		if event.Sequence != 2 {
			t.Fatalf("event sequence = %d", event.Sequence)
		}
	case <-time.After(time.Second):
		t.Fatal("polling did not recover an event after wake disconnect")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() = %v", err)
	}
}

func TestRealtimeWakeLossFaultInjection100(t *testing.T) {
	store := &fakeStore{}
	subscription := &fakeSubscription{notifications: make(chan struct{})}
	sink := channelSink{events: make(chan realtimepostgres.Event, 128), controls: make(chan Control, 4)}
	stream := productionTestStream(store, fakeWakes{subscription: subscription})
	stream.CatchUpInterval = time.Millisecond
	stream.WakeRetry = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- stream.Run(ctx, Session{TenantID: "tenant", UserID: "user", ExpiresAt: time.Now().Add(time.Hour)}, sink)
	}()
	ready := <-sink.controls
	if ready.Kind != "ready" || ready.Cursor != 0 {
		t.Fatalf("ready control=%#v", ready)
	}
	close(subscription.notifications)

	const repetitions = 100
	for sequence := uint64(1); sequence <= repetitions; sequence++ {
		// No wake hint is available. The only recovery path is a cursor-bounded
		// read from the authoritative EventStore on the periodic catch-up tick.
		store.append(realtimepostgres.Event{Sequence: sequence})
		select {
		case event := <-sink.events:
			if event.Sequence != sequence {
				t.Fatalf("sequence %d delivered %d", sequence, event.Sequence)
			}
		case <-time.After(time.Second):
			t.Fatalf("sequence %d was not recovered after lost wake", sequence)
		}
	}
	select {
	case event := <-sink.events:
		t.Fatalf("duplicate event delivered after recovery: %#v", event)
	case <-time.After(10 * time.Millisecond):
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run()=%v", err)
	}
	t.Logf("fault_injection={\"scenario\":\"realtime_wake_loss\",\"repetitions\":%d,\"wake_hints_lost\":%d,\"events_backfilled\":%d,\"contiguous_sequences\":%d,\"duplicate_deliveries\":0,\"sequence_gaps\":0,\"lost_event_facts\":0}", repetitions, repetitions, repetitions, repetitions)
}

func TestStreamRejectsCursorAheadAndSequenceGap(t *testing.T) {
	tests := []struct {
		name   string
		events []realtimepostgres.Event
		after  uint64
		want   error
	}{
		{name: "cursor ahead", events: []realtimepostgres.Event{{Sequence: 1}}, after: 2, want: ErrCursorAhead},
		{name: "gap", events: []realtimepostgres.Event{{Sequence: 1}, {Sequence: 3}}, want: ErrEventGap},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{events: test.events}
			wakes := fakeWakes{subscription: &fakeSubscription{notifications: make(chan struct{})}}
			err := productionTestStream(store, wakes).Run(context.Background(), Session{TenantID: "tenant", UserID: "user", AfterSequence: test.after, ExpiresAt: time.Now().Add(time.Hour)}, collectingSink{})
			if !errors.Is(err, test.want) {
				t.Fatalf("Run() = %v, want %v", err, test.want)
			}
		})
	}
}

func TestStreamPropagatesSlowOrFailedSink(t *testing.T) {
	sinkErr := errors.New("connection buffer exhausted")
	store := &fakeStore{events: []realtimepostgres.Event{{Sequence: 1}}}
	wakes := fakeWakes{subscription: &fakeSubscription{notifications: make(chan struct{})}}
	err := productionTestStream(store, wakes).Run(context.Background(), Session{TenantID: "tenant", UserID: "user", ExpiresAt: time.Now().Add(time.Hour)}, collectingSink{err: sinkErr})
	if !errors.Is(err, sinkErr) {
		t.Fatalf("Run() = %v, want sink failure", err)
	}
}

func TestStreamStopsBackfillAtAuthorizationExpiry(t *testing.T) {
	wakes := fakeWakes{subscription: &fakeSubscription{notifications: make(chan struct{})}}
	stream := productionTestStream(blockingStore{}, wakes)
	err := stream.Run(context.Background(), Session{TenantID: "tenant", UserID: "user", ExpiresAt: time.Now().Add(25 * time.Millisecond)}, collectingSink{})
	if !errors.Is(err, ErrAuthExpired) {
		t.Fatalf("Run() = %v, want authorization expiry", err)
	}
}

func TestStreamStopsBlockedControlFrameAtAuthorizationExpiry(t *testing.T) {
	wakes := fakeWakes{subscription: &fakeSubscription{notifications: make(chan struct{})}}
	stream := productionTestStream(&fakeStore{}, wakes)
	err := stream.Run(context.Background(), Session{TenantID: "tenant", UserID: "user", ExpiresAt: time.Now().Add(25 * time.Millisecond)}, blockingControlSink{})
	if !errors.Is(err, ErrAuthExpired) {
		t.Fatalf("Run() = %v, want authorization expiry", err)
	}
}
