package content

import (
	"context"
	"fmt"
)

func (s *Store) SaveArtifact(ctx context.Context, request SaveArtifactRequest) (ArtifactRecord, error) {
	if s == nil || s.pool == nil {
		return ArtifactRecord{}, errMissingStore
	}
	prepared, err := prepareArtifactSave(request)
	if err != nil {
		return ArtifactRecord{}, err
	}
	_, err = s.pool.Exec(ctx, `
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
		return ArtifactRecord{}, fmt.Errorf("save content artifact: %w", err)
	}
	return s.GetArtifact(ctx, request.Artifact.ContentKey)
}
