package outline

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func (s *Store) SaveOutline(ctx context.Context, request SaveOutlineRequest) (OutlineRecord, error) {
	if s == nil || s.pool == nil {
		return OutlineRecord{}, errMissingStore
	}
	prepared, err := prepareOutlineSave(request)
	if err != nil {
		return OutlineRecord{}, err
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OutlineRecord{}, fmt.Errorf("begin save outline: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	if err := saveOutlineTx(ctx, tx, prepared); err != nil {
		return OutlineRecord{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return OutlineRecord{}, fmt.Errorf("commit save outline: %w", err)
	}
	committed = true
	return s.GetOutline(ctx, request.UserID, request.Outline.OutlineID)
}
