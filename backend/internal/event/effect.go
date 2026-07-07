package event

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func applyEffects(ctx context.Context, tx pgx.Tx, effects []TxEffect) error {
	for i, effect := range effects {
		if err := effect(ctx, tx); err != nil {
			return fmt.Errorf("apply append effect %d: %w", i, err)
		}
	}
	return nil
}
