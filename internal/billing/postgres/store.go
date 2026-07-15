// Package postgres implements the hard-cap usage accounting boundary. Credit
// bucket mutation, reservation state, immutable ledger entry and domain event
// are committed in one transaction.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/platform/ids"
)

var (
	ErrConfiguration       = errors.New("usage accounting store configuration is invalid")
	ErrInvalidCommand      = errors.New("usage accounting command is invalid")
	ErrStaleEpoch          = errors.New("usage accounting store epoch is stale")
	ErrInsufficientCredits = errors.New("credit bucket hard limit exceeded")
	ErrReservationConflict = errors.New("usage reservation conflicts with durable state")
	ErrUnsafeRelease       = errors.New("dispatched usage reservation cannot be released")
)

type EpochAuthority interface {
	CurrentStoreEpoch(context.Context) (string, error)
}

type Store struct {
	Pool       *pgxpool.Pool
	Appender   eventpostgres.Appender
	Epochs     EpochAuthority
	StoreEpoch string
	IDKey      []byte
	Now        func() time.Time
}

type PayloadPointer struct{ Ref, Hash string }

func (store Store) Valid() bool {
	return store.Pool != nil && store.Epochs != nil && store.StoreEpoch != "" && len(store.IDKey) >= 32
}

func (store Store) Compatible(pool *pgxpool.Pool, epoch string) bool {
	return store.Valid() && store.Pool == pool && store.StoreEpoch == epoch
}

func (store Store) RequireEpoch(ctx context.Context) error {
	epoch, err := store.Epochs.CurrentStoreEpoch(ctx)
	if err != nil || epoch == "" {
		return ErrConfiguration
	}
	if epoch != store.StoreEpoch {
		return ErrStaleEpoch
	}
	return nil
}

func (store Store) now() time.Time {
	now := time.Now().UTC()
	if store.Now != nil {
		now = store.Now().UTC()
	}
	return now.Truncate(time.Microsecond)
}

type eventIDs struct{ event, outbox, publish, ledger string }

func (store Store) identifiers(scope, reservationID string, version uint64) (eventIDs, error) {
	values := make([]string, 4)
	for index, part := range []string{"event", "outbox", "publish", "ledger"} {
		value, err := ids.DeterministicUUID(store.IDKey, "usage-"+scope+":"+part, reservationID+":"+strconv.FormatUint(version, 10))
		if err != nil {
			return eventIDs{}, err
		}
		values[index] = value
	}
	return eventIDs{values[0], values[1], values[2], values[3]}, nil
}

func usageEvent(identifier eventIDs, event eventpostgres.Event, pointer PayloadPointer) eventpostgres.Input {
	event.ID, event.PayloadRef, event.PayloadHash = identifier.event, pointer.Ref, pointer.Hash
	return eventpostgres.Input{Event: event, Commands: []eventpostgres.OutboxCommand{{ID: identifier.outbox, CommandID: identifier.publish, CommandType: "events.publish", PayloadRef: pointer.Ref, PayloadHash: pointer.Hash}}}
}

func validPointer(pointer PayloadPointer) bool { return pointer.Ref != "" && pointer.Hash != "" }

func validActor(actor json.RawMessage) bool {
	var value map[string]any
	return json.Unmarshal(actor, &value) == nil && value != nil
}
