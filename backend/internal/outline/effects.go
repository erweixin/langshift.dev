package outline

import (
	"context"

	"github.com/jackc/pgx/v5"

	"lites/backend/internal/event"
)

func (s *Store) SaveOutlineEffect(request SaveOutlineRequest) (event.TxEffect, OutlineRecord, error) {
	prepared, err := prepareOutlineSave(request)
	if err != nil {
		return nil, OutlineRecord{}, err
	}
	return func(ctx context.Context, tx pgx.Tx) error {
		return saveOutlineTx(ctx, tx, prepared)
	}, prepared.record, nil
}
