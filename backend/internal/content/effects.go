package content

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"lites/backend/internal/event"
)

func (s *Store) SaveArtifactEffect(request SaveArtifactRequest) (event.TxEffect, ArtifactRecord, error) {
	prepared, err := prepareArtifactSave(request)
	if err != nil {
		return nil, ArtifactRecord{}, err
	}
	return func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO content_artifacts (
				content_key,
				artifact,
				artifact_hash,
				task_template_id,
				target_stack,
				level_band,
				content_version,
				prompt_version,
				review_status,
				validation_attempts,
				llm_ledger_id,
				source_run_id,
				source_attempt_key
			)
			VALUES (
				$1,
				$2::jsonb,
				$3,
				$4,
				$5,
				$6,
				$7,
				$8,
				$9,
				$10,
				nullif($11, ''),
				nullif($12, ''),
				nullif($13, '')
			)
			ON CONFLICT (content_key) DO NOTHING
		`, prepared.args()...)
		if err != nil {
			return fmt.Errorf("save content artifact: %w", err)
		}
		return nil
	}, prepared.record, nil
}
