-- +goose Up
CREATE TABLE learning_outlines (
  outline_id text PRIMARY KEY,
  user_id text NOT NULL,
  source_run_id text NOT NULL UNIQUE,
  request jsonb NOT NULL,
  outline jsonb NOT NULL,
  llm_ledger_id text,
  source_attempt_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE learning_outlines IS 'Personalized learning roadmap facts generated from user information and requirements.';
COMMENT ON COLUMN learning_outlines.outline IS 'Structured LearningOutline JSON. It can reference user requirements and must not be reused across users as generic content.';

CREATE TABLE learning_tasks (
  task_id text PRIMARY KEY,
  outline_id text NOT NULL REFERENCES learning_outlines(outline_id) ON DELETE CASCADE,
  user_id text NOT NULL,
  day_index integer NOT NULL,
  task_template_id text NOT NULL,
  target_stack text NOT NULL,
  level_band text NOT NULL,
  title text NOT NULL,
  judge text NOT NULL,
  minutes integer NOT NULL,
  status text NOT NULL DEFAULT 'pending',
  content_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT learning_tasks_day_index_positive CHECK (day_index > 0),
  CONSTRAINT learning_tasks_minutes_positive CHECK (minutes > 0),
  CONSTRAINT learning_tasks_status_valid CHECK (
    status IN ('pending', 'active', 'done', 'downgraded')
  ),
  CONSTRAINT learning_tasks_outline_day_unique UNIQUE (outline_id, day_index)
);

COMMENT ON TABLE learning_tasks IS 'User-scoped TaskSpecs derived from a LearningOutline. Content generation consumes these tasks later.';

CREATE INDEX learning_outlines_user_created_at_idx ON learning_outlines (user_id, created_at DESC);
CREATE INDEX learning_tasks_user_status_day_idx ON learning_tasks (user_id, status, day_index);
CREATE INDEX learning_tasks_content_key_idx ON learning_tasks (content_key)
  WHERE content_key IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS learning_tasks;
DROP TABLE IF EXISTS learning_outlines;
