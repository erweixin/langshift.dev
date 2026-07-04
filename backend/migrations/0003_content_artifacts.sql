-- +goose Up
CREATE TABLE content_artifacts (
  content_key text PRIMARY KEY,
  artifact jsonb NOT NULL,
  artifact_hash text NOT NULL,
  task_template_id text NOT NULL,
  target_stack text NOT NULL,
  level_band text NOT NULL,
  content_version integer NOT NULL,
  prompt_version integer NOT NULL,
  review_status text NOT NULL DEFAULT 'auto_ok',
  validation_attempts integer NOT NULL DEFAULT 0,
  llm_ledger_id text,
  source_run_id text,
  source_attempt_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT content_artifacts_content_version_positive CHECK (content_version > 0),
  CONSTRAINT content_artifacts_prompt_version_positive CHECK (prompt_version > 0),
  CONSTRAINT content_artifacts_validation_attempts_nonnegative CHECK (validation_attempts >= 0),
  CONSTRAINT content_artifacts_review_status_valid CHECK (
    review_status IN ('auto_ok', 'needs_review', 'human_ok', 'rejected')
  ),
  CONSTRAINT content_artifacts_dimensions_unique UNIQUE (
    task_template_id,
    target_stack,
    level_band,
    content_version,
    prompt_version
  )
);

COMMENT ON TABLE content_artifacts IS 'Fact source for generated reusable content artifacts. EventStore records only content_key references; replay must not clear this table.';
COMMENT ON COLUMN content_artifacts.artifact IS 'Server-side artifact JSON. It may include reference_solution and must not be returned to learners without redaction.';
COMMENT ON COLUMN content_artifacts.artifact_hash IS 'SHA-256 hash of the stored artifact JSON.';
COMMENT ON COLUMN content_artifacts.content_key IS 'Stable cache key derived from task_template_id, target_stack, level_band, content_version, and prompt_version.';

CREATE INDEX content_artifacts_review_status_created_at_idx ON content_artifacts (review_status, created_at DESC);
CREATE INDEX content_artifacts_source_run_id_idx ON content_artifacts (source_run_id)
  WHERE source_run_id IS NOT NULL;
CREATE INDEX content_artifacts_llm_ledger_id_idx ON content_artifacts (llm_ledger_id)
  WHERE llm_ledger_id IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS content_artifacts;
