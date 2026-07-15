package natswake

import (
	"context"
	"testing"
	"time"

	"github.com/langshift/lites/internal/eventbus/natsjs"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
)

type fakeBrokerSubscription struct{ closed bool }

func (subscription *fakeBrokerSubscription) Unsubscribe() error {
	subscription.closed = true
	return nil
}

type fakeSubscriber struct {
	subject  string
	callback func([]byte, string)
	broker   *fakeBrokerSubscription
}

func (subscriber *fakeSubscriber) Subscribe(subject string, callback func([]byte, string)) (BrokerSubscription, error) {
	subscriber.subject, subscriber.callback = subject, callback
	subscriber.broker = &fakeBrokerSubscription{}
	return subscriber.broker, nil
}

func TestHubIsolatesTenantsCoalescesWakesAndRejectsMalformedEnvelopes(t *testing.T) {
	broker := &fakeSubscriber{}
	hub, err := New(broker)
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	tenantA, err := hub.Subscribe(context.Background(), "tenant-a", "user-a")
	if err != nil {
		t.Fatal(err)
	}
	tenantB, err := hub.Subscribe(context.Background(), "tenant-b", "user-b")
	if err != nil {
		t.Fatal(err)
	}

	broker.callback([]byte(`{"version":1,"tenant_id":"tenant-a"}`), broker.subject)
	assertNoWake(t, tenantA.Notifications())
	body, subject, err := natsjs.Encode(eventpostgres.PublishedCommand{TenantID: "tenant-a", StoreEpoch: "epoch", CommandID: "command", CommandType: "events.publish", AggregateKind: "run", AggregateID: "run-1", PayloadRef: "payload", PayloadHash: "hash"})
	if err != nil {
		t.Fatal(err)
	}
	broker.callback(body, subject)
	broker.callback(body, subject)
	select {
	case <-tenantA.Notifications():
	case <-time.After(time.Second):
		t.Fatal("tenant A did not receive a wake")
	}
	assertNoWake(t, tenantA.Notifications())
	assertNoWake(t, tenantB.Notifications())
}

func TestHubClosesBrokerAndLocalSubscriptions(t *testing.T) {
	broker := &fakeSubscriber{}
	hub, err := New(broker)
	if err != nil {
		t.Fatal(err)
	}
	local, err := hub.Subscribe(context.Background(), "tenant", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err = hub.Close(); err != nil {
		t.Fatal(err)
	}
	if !broker.broker.closed {
		t.Fatal("broker subscription remained open")
	}
	if _, open := <-local.Notifications(); open {
		t.Fatal("local subscription remained open")
	}
	if _, err = hub.Subscribe(context.Background(), "tenant", "user"); err != ErrClosed {
		t.Fatalf("Subscribe() error = %v", err)
	}
}

func assertNoWake(t *testing.T, notifications <-chan struct{}) {
	t.Helper()
	select {
	case <-notifications:
		t.Fatal("unexpected wake")
	case <-time.After(20 * time.Millisecond):
	}
}
