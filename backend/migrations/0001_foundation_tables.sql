-- +goose Up
CREATE TABLE events (
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
  CONSTRAINT events_seq_positive CHECK (seq > 0),
  CONSTRAINT events_schema_version_positive CHECK (schema_version > 0),
  CONSTRAINT events_user_seq_unique UNIQUE (user_id, seq)
);

CREATE INDEX events_user_created_at_idx ON events (user_id, created_at);
CREATE INDEX events_user_seq_idx ON events (user_id, seq);
CREATE INDEX events_type_idx ON events (type);
CREATE INDEX events_run_id_idx ON events (run_id) WHERE run_id IS NOT NULL;
CREATE INDEX events_task_id_idx ON events (task_id) WHERE task_id IS NOT NULL;
CREATE INDEX events_command_id_idx ON events (command_id) WHERE command_id IS NOT NULL;

CREATE TABLE event_cursors (
  user_id text PRIMARY KEY,
  current_seq bigint NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT event_cursors_current_seq_nonnegative CHECK (current_seq >= 0)
);

CREATE TABLE idempotency_keys (
  user_id text NOT NULL,
  scope text NOT NULL,
  key text NOT NULL,
  command_id text NOT NULL,
  request_hash text NOT NULL,
  response_status integer,
  response_body jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT idempotency_keys_response_status_valid CHECK (
    response_status IS NULL OR response_status BETWEEN 100 AND 599
  ),
  CONSTRAINT idempotency_keys_pk UNIQUE (user_id, scope, key),
  CONSTRAINT idempotency_keys_command_unique UNIQUE (command_id)
);

CREATE INDEX idempotency_keys_created_at_idx ON idempotency_keys (created_at);

CREATE TABLE jobs (
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
  CONSTRAINT jobs_command_unique UNIQUE (command_id),
  CONSTRAINT jobs_attempts_nonnegative CHECK (attempts >= 0),
  CONSTRAINT jobs_status_valid CHECK (
    status IN ('queued', 'leased', 'done', 'failed', 'dead', 'cancelled')
  ),
  CONSTRAINT jobs_lease_fields_match CHECK (
    (status <> 'leased')
    OR (lease_until IS NOT NULL AND lease_token IS NOT NULL AND leased_by IS NOT NULL)
  )
);

CREATE INDEX jobs_status_due_at_idx ON jobs (status, due_at);
CREATE INDEX jobs_kind_status_due_at_idx ON jobs (kind, status, due_at);
CREATE INDEX jobs_subject_user_id_idx ON jobs (subject_user_id) WHERE subject_user_id IS NOT NULL;
CREATE INDEX jobs_lease_until_idx ON jobs (lease_until) WHERE lease_until IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS jobs;
DROP TABLE IF EXISTS idempotency_keys;
DROP TABLE IF EXISTS event_cursors;
DROP TABLE IF EXISTS events;
