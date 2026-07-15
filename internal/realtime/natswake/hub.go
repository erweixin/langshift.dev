// Package natswake turns the durable events.publish command into a coalesced
// tenant-level wake-up. It deliberately does not decode event payloads or
// claim delivery: every connection re-reads its user cursor from EventStore.
package natswake

import (
	"context"
	"errors"
	"sync"

	"github.com/langshift/lites/internal/eventbus/natsjs"
	"github.com/langshift/lites/internal/realtime"
	"github.com/nats-io/nats.go"
)

var (
	ErrConfiguration = errors.New("realtime NATS wake hub is not configured")
	ErrClosed        = errors.New("realtime NATS wake hub is closed")
)

type BrokerSubscription interface{ Unsubscribe() error }

type Subscriber interface {
	Subscribe(string, func([]byte, string)) (BrokerSubscription, error)
}

type NATSSubscriber struct{ Connection *nats.Conn }

func (subscriber NATSSubscriber) Subscribe(subject string, callback func([]byte, string)) (BrokerSubscription, error) {
	if subscriber.Connection == nil || callback == nil {
		return nil, ErrConfiguration
	}
	return subscriber.Connection.Subscribe(subject, func(message *nats.Msg) {
		callback(message.Data, message.Subject)
	})
}

type Hub struct {
	mu          sync.Mutex
	closed      bool
	nextID      uint64
	connections map[string]map[uint64]*subscription
	broker      BrokerSubscription
}

func New(subscriber Subscriber) (*Hub, error) {
	if subscriber == nil {
		return nil, ErrConfiguration
	}
	subject, err := natsjs.SubjectFor("events.publish")
	if err != nil {
		return nil, err
	}
	hub := &Hub{connections: make(map[string]map[uint64]*subscription)}
	broker, err := subscriber.Subscribe(subject, hub.notify)
	if err != nil || broker == nil {
		return nil, ErrConfiguration
	}
	hub.broker = broker
	return hub, nil
}

func (hub *Hub) Subscribe(ctx context.Context, tenantID, userID string) (realtime.WakeSubscription, error) {
	if hub == nil || ctx == nil || tenantID == "" || userID == "" {
		return nil, ErrConfiguration
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed {
		return nil, ErrClosed
	}
	hub.nextID++
	current := &subscription{hub: hub, tenantID: tenantID, id: hub.nextID, notifications: make(chan struct{}, 1)}
	if hub.connections[tenantID] == nil {
		hub.connections[tenantID] = make(map[uint64]*subscription)
	}
	hub.connections[tenantID][current.id] = current
	return current, nil
}

func (hub *Hub) notify(body []byte, subject string) {
	command, err := natsjs.Decode(body, subject)
	if err != nil {
		return
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed {
		return
	}
	for _, connection := range hub.connections[command.TenantID] {
		select {
		case connection.notifications <- struct{}{}:
		default:
			// Wake-ups coalesce. The next catch-up reads the full DB range.
		}
	}
}

func (hub *Hub) Close() error {
	if hub == nil {
		return nil
	}
	hub.mu.Lock()
	if hub.closed {
		hub.mu.Unlock()
		return nil
	}
	hub.closed = true
	for tenantID, connections := range hub.connections {
		for _, connection := range connections {
			connection.closed = true
			close(connection.notifications)
		}
		delete(hub.connections, tenantID)
	}
	broker := hub.broker
	hub.broker = nil
	hub.mu.Unlock()
	if broker != nil {
		return broker.Unsubscribe()
	}
	return nil
}

type subscription struct {
	hub           *Hub
	tenantID      string
	id            uint64
	notifications chan struct{}
	closed        bool
}

func (subscription *subscription) Notifications() <-chan struct{} { return subscription.notifications }

func (subscription *subscription) Close() error {
	if subscription == nil || subscription.hub == nil {
		return nil
	}
	hub := subscription.hub
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if subscription.closed {
		return nil
	}
	subscription.closed = true
	if connections := hub.connections[subscription.tenantID]; connections != nil {
		delete(connections, subscription.id)
		if len(connections) == 0 {
			delete(hub.connections, subscription.tenantID)
		}
	}
	close(subscription.notifications)
	return nil
}
