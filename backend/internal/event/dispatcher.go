package event

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type NoopDispatcher struct{}

func (NoopDispatcher) Apply(context.Context, pgx.Tx, StoredEvent) error {
	return nil
}
