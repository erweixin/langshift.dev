-- +goose Up
CREATE TABLE agent_events (
  event_id text PRIMARY KEY,
  seq bigint NOT NULL,
  type text NOT NULL,
  schema_version integer NOT NULL,
  user_id text NOT NULL,
  mission_id text,
  task_id text,
  run_id text,
  command_id text,
  causation_id text,
  correlation_id text,
  created_at timestamptz NOT NULL DEFAULT now(),
  payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  CONSTRAINT agent_events_seq_positive CHECK (seq > 0),
  CONSTRAINT agent_events_schema_version_positive CHECK (schema_version > 0),
  CONSTRAINT agent_events_user_seq_unique UNIQUE (user_id, seq)
);

CREATE INDEX agent_events_user_created_at_idx ON agent_events (user_id, created_at);
CREATE INDEX agent_events_user_seq_idx ON agent_events (user_id, seq);
CREATE INDEX agent_events_type_idx ON agent_events (type);
CREATE INDEX agent_events_run_id_idx ON agent_events (run_id) WHERE run_id IS NOT NULL;
CREATE INDEX agent_events_task_id_idx ON agent_events (task_id) WHERE task_id IS NOT NULL;
CREATE INDEX agent_events_command_id_idx ON agent_events (command_id) WHERE command_id IS NOT NULL;

CREATE TABLE agent_event_cursors (
  user_id text PRIMARY KEY,
  current_seq bigint NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT agent_event_cursors_current_seq_nonnegative CHECK (current_seq >= 0)
);

CREATE TABLE agent_idempotency_keys (
  user_id text NOT NULL,
  scope text NOT NULL,
  key text NOT NULL,
  command_id text NOT NULL,
  request_hash text NOT NULL,
  response_status integer,
  response_body jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT agent_idempotency_keys_response_status_valid CHECK (
    response_status IS NULL OR response_status BETWEEN 100 AND 599
  ),
  CONSTRAINT agent_idempotency_keys_pk UNIQUE (user_id, scope, key),
  CONSTRAINT agent_idempotency_keys_command_unique UNIQUE (command_id)
);

CREATE INDEX agent_idempotency_keys_created_at_idx ON agent_idempotency_keys (created_at);

CREATE TABLE agent_jobs (
  job_id text PRIMARY KEY,
  command_id text NOT NULL,
  kind text NOT NULL,
  subject_user_id text,
  payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  status text NOT NULL DEFAULT 'queued',
  attempts integer NOT NULL DEFAULT 0,
  lease_until timestamptz,
  lease_token text,
  leased_by text,
  heartbeat_at timestamptz,
  due_at timestamptz NOT NULL DEFAULT now(),
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT agent_jobs_command_unique UNIQUE (command_id),
  CONSTRAINT agent_jobs_attempts_nonnegative CHECK (attempts >= 0),
  CONSTRAINT agent_jobs_status_valid CHECK (
    status IN ('queued', 'leased', 'done', 'failed', 'dead', 'cancelled')
  ),
  CONSTRAINT agent_jobs_lease_fields_match CHECK (
    (status <> 'leased')
    OR (lease_until IS NOT NULL AND lease_token IS NOT NULL AND leased_by IS NOT NULL)
  )
);

CREATE INDEX agent_jobs_status_due_at_idx ON agent_jobs (status, due_at);
CREATE INDEX agent_jobs_kind_status_due_at_idx ON agent_jobs (kind, status, due_at);
CREATE INDEX agent_jobs_subject_user_id_idx ON agent_jobs (subject_user_id) WHERE subject_user_id IS NOT NULL;
CREATE INDEX agent_jobs_lease_until_idx ON agent_jobs (lease_until) WHERE lease_until IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS agent_jobs;
DROP TABLE IF EXISTS agent_idempotency_keys;
DROP TABLE IF EXISTS agent_event_cursors;
DROP TABLE IF EXISTS agent_events;
