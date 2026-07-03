-- +goose Up
ALTER TABLE event_cursors
  RENAME COLUMN current_seq TO next_seq;

-- 0001 stored the last allocated seq (default 0). Under next-to-allocate
-- semantics existing values shift by +1 and the floor becomes 1, matching
-- events_seq_positive CHECK (seq > 0).
UPDATE event_cursors SET next_seq = next_seq + 1;

ALTER TABLE event_cursors
  ALTER COLUMN next_seq SET DEFAULT 1;

ALTER TABLE event_cursors
  DROP CONSTRAINT event_cursors_current_seq_nonnegative;

ALTER TABLE event_cursors
  ADD CONSTRAINT event_cursors_next_seq_positive CHECK (next_seq > 0);

COMMENT ON COLUMN event_cursors.next_seq IS 'Next user-scoped event seq to allocate. Writers lock this row with SELECT ... FOR UPDATE before appending events.';

ALTER TABLE events
  ADD COLUMN conversation_id text;

COMMENT ON COLUMN events.conversation_id IS 'Optional conversation projection key. Event seq remains user-scoped; conversation_id is only a filter dimension.';

ALTER TABLE events
  ADD COLUMN tenant_id text;

COMMENT ON COLUMN events.tenant_id IS 'Reserved for stage-6 multi-tenancy. Nullable until RLS lands; reserved now to avoid backfilling the append-only hot table.';

ALTER TABLE jobs
  ADD COLUMN store_epoch bigint;

COMMENT ON COLUMN jobs.store_epoch IS 'Reserved for stage-4 PITR recovery generation. Consumers will reject commands whose epoch is below the current store epoch.';

CREATE INDEX events_conversation_seq_idx ON events (user_id, conversation_id, seq)
  WHERE conversation_id IS NOT NULL;
CREATE INDEX events_conversation_created_at_idx ON events (user_id, conversation_id, created_at)
  WHERE conversation_id IS NOT NULL;

CREATE TABLE runs (
  run_id text PRIMARY KEY,
  user_id text NOT NULL,
  conversation_id text,
  run_type text NOT NULL,
  status text NOT NULL,
  run_version integer NOT NULL DEFAULT 0,
  input_ref jsonb NOT NULL DEFAULT '{}'::jsonb,
  error jsonb,
  due_at timestamptz,
  started_at timestamptz,
  finished_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT runs_run_version_nonnegative CHECK (run_version >= 0),
  CONSTRAINT runs_type_valid CHECK (
    run_type IN ('chat_turn', 'diagnosis', 'task_gen', 'lesson_gen', 'review', 'reentry')
  ),
  CONSTRAINT runs_status_valid CHECK (
    status IN (
      'accepted',
      'queued',
      'executing',
      'waiting_tool',
      'waiting_approval',
      'succeeded',
      'failed',
      'expired',
      'cancelled'
    )
  ),
  CONSTRAINT runs_finished_after_created CHECK (
    finished_at IS NULL OR finished_at >= created_at
  ),
  CONSTRAINT runs_started_after_created CHECK (
    started_at IS NULL OR started_at >= created_at
  )
);

COMMENT ON TABLE runs IS 'Rebuildable run projection. Mutations must use run_version CAS; llm_ledger references run_id softly because ledger is not replay-cleared.';
COMMENT ON COLUMN runs.conversation_id IS 'Optional conversation that owns this run. Background runs may leave this NULL.';
COMMENT ON COLUMN runs.run_version IS 'Optimistic lock for Run state transitions. Every legal transition increments this value.';
COMMENT ON COLUMN runs.input_ref IS 'Small references to existing facts, such as event_id, evidence_id, message_id, or content_key. Do not store large prompts here.';
COMMENT ON COLUMN runs.error IS 'Structured terminal error for failed or expired runs.';
COMMENT ON CONSTRAINT runs_status_valid ON runs IS 'Run state machine states. Terminal states are succeeded, failed, expired, and cancelled.';

CREATE INDEX runs_user_created_at_idx ON runs (user_id, created_at DESC);
CREATE INDEX runs_user_status_due_at_idx ON runs (user_id, status, due_at);
CREATE INDEX runs_user_conversation_created_at_idx ON runs (user_id, conversation_id, created_at DESC)
  WHERE conversation_id IS NOT NULL;
CREATE INDEX runs_type_status_idx ON runs (run_type, status);
CREATE INDEX runs_active_due_at_idx ON runs (due_at)
  WHERE status IN ('accepted', 'queued', 'executing') AND due_at IS NOT NULL;
CREATE INDEX runs_waiting_due_at_idx ON runs (due_at)
  WHERE status IN ('waiting_tool', 'waiting_approval') AND due_at IS NOT NULL;

CREATE TABLE conversations (
  conversation_id text PRIMARY KEY,
  user_id text NOT NULL,
  title text,
  status text NOT NULL DEFAULT 'active',
  active_run_id text,
  last_run_id text,
  last_message_id text,
  last_message_at timestamptz,
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT conversations_status_valid CHECK (
    status IN ('active', 'archived', 'deleted')
  ),
  CONSTRAINT conversations_last_message_after_created CHECK (
    last_message_at IS NULL OR last_message_at >= created_at
  )
);

COMMENT ON TABLE conversations IS 'Rebuildable conversation projection. It owns conversation shell state and recovery pointers, not event ordering.';
COMMENT ON COLUMN conversations.active_run_id IS 'Current non-terminal run for resume/reconnect flows. NULL when the conversation has no active run.';
COMMENT ON COLUMN conversations.last_message_at IS 'Latest user-visible message time for ordering conversation lists.';

CREATE INDEX conversations_user_status_updated_at_idx ON conversations (user_id, status, updated_at DESC);
CREATE INDEX conversations_user_last_message_at_idx ON conversations (user_id, last_message_at DESC NULLS LAST);
CREATE INDEX conversations_active_run_id_idx ON conversations (active_run_id)
  WHERE active_run_id IS NOT NULL;

CREATE TABLE llm_ledger (
  id text PRIMARY KEY,
  tenant_id text,
  user_id text NOT NULL,
  run_id text,
  surface text NOT NULL,
  provider text NOT NULL,
  model text NOT NULL,
  status text NOT NULL,
  tok_in integer NOT NULL DEFAULT 0,
  tok_out integer NOT NULL DEFAULT 0,
  max_tok_out integer NOT NULL DEFAULT 0,
  cache_read integer NOT NULL DEFAULT 0,
  cost_usd numeric(14, 8) NOT NULL DEFAULT 0,
  estimated_cost_usd numeric(14, 8) NOT NULL DEFAULT 0,
  cost_basis text NOT NULL,
  attempt_key text NOT NULL,
  request_hash text NOT NULL,
  prompt_version text NOT NULL,
  context_manifest jsonb NOT NULL DEFAULT '{}'::jsonb,
  provider_request_id text,
  error_code text,
  error_message text,
  result_hash text,
  started_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT llm_ledger_attempt_key_unique UNIQUE (attempt_key),
  CONSTRAINT llm_ledger_status_valid CHECK (
    status IN ('pending', 'ok', 'failed_no_charge', 'provider_error', 'unknown', 'cancelled')
  ),
  CONSTRAINT llm_ledger_cost_basis_valid CHECK (
    cost_basis IN ('actual', 'estimated', 'zero')
  ),
  CONSTRAINT llm_ledger_tokens_nonnegative CHECK (
    tok_in >= 0 AND tok_out >= 0 AND max_tok_out >= 0 AND cache_read >= 0
  ),
  CONSTRAINT llm_ledger_costs_nonnegative CHECK (
    cost_usd >= 0 AND estimated_cost_usd >= 0
  ),
  CONSTRAINT llm_ledger_finished_after_started CHECK (
    finished_at IS NULL OR finished_at >= started_at
  ),
  CONSTRAINT llm_ledger_pending_unfinished CHECK (
    status <> 'pending' OR finished_at IS NULL
  )
);

COMMENT ON TABLE llm_ledger IS 'Fact ledger for every LLM attempt. Written before provider calls and settled after response, failure, or unknown outcome.';
COMMENT ON COLUMN llm_ledger.attempt_key IS 'Stable unique key for one provider attempt. Retries use a new attempt_key.';
COMMENT ON COLUMN llm_ledger.request_hash IS 'Fingerprint of the provider input, used to explain and de-duplicate ledger writes.';
COMMENT ON COLUMN llm_ledger.context_manifest IS 'Inline manifest of prompt version, input facts, summaries, content keys, and other context used to explain why the call happened.';
COMMENT ON COLUMN llm_ledger.cost_basis IS 'actual for settled usage, estimated for unknown/pending provider outcomes, zero for calls that never crossed the provider boundary.';
COMMENT ON COLUMN llm_ledger.tenant_id IS 'Reserved for stage-6 multi-tenancy. Nullable until RLS lands; reserved now to avoid backfilling the fact ledger.';

CREATE INDEX llm_ledger_user_started_at_idx ON llm_ledger (user_id, started_at DESC);
CREATE INDEX llm_ledger_run_id_idx ON llm_ledger (run_id) WHERE run_id IS NOT NULL;
CREATE INDEX llm_ledger_status_started_at_idx ON llm_ledger (status, started_at);
CREATE INDEX llm_ledger_provider_model_started_at_idx ON llm_ledger (provider, model, started_at DESC);
CREATE INDEX llm_ledger_billable_idx ON llm_ledger (user_id, started_at DESC)
  WHERE status IN ('ok', 'unknown');

CREATE TABLE run_messages (
  message_id text PRIMARY KEY,
  user_id text NOT NULL,
  run_id text NOT NULL,
  conversation_id text,
  role text NOT NULL,
  status text NOT NULL DEFAULT 'streaming',
  content_type text NOT NULL DEFAULT 'text',
  content text NOT NULL DEFAULT '',
  llm_ledger_id text,
  source_event_id text,
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  completed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT run_messages_role_valid CHECK (
    role IN ('system', 'user', 'assistant', 'tool')
  ),
  CONSTRAINT run_messages_status_valid CHECK (
    status IN ('streaming', 'completed', 'failed', 'cancelled')
  ),
  CONSTRAINT run_messages_content_type_valid CHECK (
    content_type IN ('text', 'markdown', 'json')
  ),
  CONSTRAINT run_messages_completed_at_valid CHECK (
    (status = 'completed' AND completed_at IS NOT NULL)
    OR (status <> 'completed')
  )
);

COMMENT ON TABLE run_messages IS 'User-visible message projection for a run. Streaming chunks aggregate into content when completed.';
COMMENT ON COLUMN run_messages.llm_ledger_id IS 'Soft reference to the LLM attempt that produced this message, when applicable.';
COMMENT ON COLUMN run_messages.source_event_id IS 'Event that finalized or recorded this message. Used for replay idempotency.';

CREATE INDEX run_messages_run_created_at_idx ON run_messages (run_id, created_at);
CREATE INDEX run_messages_user_conversation_created_at_idx ON run_messages (user_id, conversation_id, created_at)
  WHERE conversation_id IS NOT NULL;
CREATE INDEX run_messages_llm_ledger_id_idx ON run_messages (llm_ledger_id)
  WHERE llm_ledger_id IS NOT NULL;
CREATE INDEX run_messages_source_event_id_idx ON run_messages (source_event_id)
  WHERE source_event_id IS NOT NULL;

CREATE TABLE run_message_chunks (
  chunk_id text PRIMARY KEY,
  tenant_id text,
  user_id text NOT NULL,
  run_id text NOT NULL,
  message_id text NOT NULL,
  seq bigint NOT NULL,
  kind text NOT NULL,
  delta text NOT NULL DEFAULT '',
  payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT run_message_chunks_seq_positive CHECK (seq > 0),
  CONSTRAINT run_message_chunks_run_seq_unique UNIQUE (run_id, seq),
  CONSTRAINT run_message_chunks_kind_valid CHECK (
    kind IN (
      'message_start',
      'text_delta',
      'tool_call_delta',
      'tool_result',
      'error',
      'done'
    )
  )
);

COMMENT ON TABLE run_message_chunks IS 'Recoverable per-run stream log for SSE backfill. This is not the EventStore.';
COMMENT ON COLUMN run_message_chunks.seq IS 'Run-scoped stream sequence. Frontend reconnects with after_seq and the server backfills seq greater than it.';
COMMENT ON COLUMN run_message_chunks.delta IS 'Small text delta for text_delta chunks. Structured chunks use payload.';
COMMENT ON COLUMN run_message_chunks.tenant_id IS 'Reserved for stage-6 multi-tenancy. Nullable until RLS lands; reserved now to avoid backfilling the highest insert-rate table.';

CREATE INDEX run_message_chunks_run_seq_idx ON run_message_chunks (run_id, seq);
CREATE INDEX run_message_chunks_message_seq_idx ON run_message_chunks (message_id, seq);
CREATE INDEX run_message_chunks_user_created_at_idx ON run_message_chunks (user_id, created_at DESC);

CREATE TABLE tool_calls (
  tool_call_id text PRIMARY KEY,
  user_id text NOT NULL,
  run_id text NOT NULL,
  conversation_id text,
  tool_name text NOT NULL,
  status text NOT NULL DEFAULT 'requested',
  tool_call_version integer NOT NULL DEFAULT 0,
  risk text NOT NULL DEFAULT 'normal',
  requires_approval boolean NOT NULL DEFAULT false,
  args jsonb NOT NULL DEFAULT '{}'::jsonb,
  result jsonb,
  error jsonb,
  requested_event_id text,
  result_event_id text,
  approval_event_id text,
  due_at timestamptz,
  started_at timestamptz,
  finished_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT tool_calls_version_nonnegative CHECK (tool_call_version >= 0),
  CONSTRAINT tool_calls_status_valid CHECK (
    status IN (
      'requested',
      'waiting_approval',
      'approved',
      'rejected',
      'executing',
      'succeeded',
      'failed',
      'outcome_unknown',
      'cancelled',
      'expired'
    )
  ),
  CONSTRAINT tool_calls_risk_valid CHECK (
    risk IN ('normal', 'sensitive', 'destructive')
  ),
  CONSTRAINT tool_calls_finished_after_created CHECK (
    finished_at IS NULL OR finished_at >= created_at
  ),
  CONSTRAINT tool_calls_started_after_created CHECK (
    started_at IS NULL OR started_at >= created_at
  )
);

COMMENT ON TABLE tool_calls IS 'Rebuildable current-state projection for tool calls, including approval and outcome tracking.';
COMMENT ON COLUMN tool_calls.tool_call_version IS 'Optimistic lock for ToolCall state transitions.';
COMMENT ON COLUMN tool_calls.requires_approval IS 'True when policy requires human approval before execution.';
COMMENT ON COLUMN tool_calls.args IS 'Validated tool arguments. Large inputs should be stored by reference.';
COMMENT ON COLUMN tool_calls.result IS 'Small structured result or a reference to an artifact.';

CREATE INDEX tool_calls_run_created_at_idx ON tool_calls (run_id, created_at);
CREATE INDEX tool_calls_user_status_due_at_idx ON tool_calls (user_id, status, due_at);
CREATE INDEX tool_calls_waiting_approval_idx ON tool_calls (user_id, due_at)
  WHERE status = 'waiting_approval';
CREATE INDEX tool_calls_conversation_created_at_idx ON tool_calls (user_id, conversation_id, created_at)
  WHERE conversation_id IS NOT NULL;

CREATE TABLE evidence (
  evidence_id text PRIMARY KEY,
  user_id text NOT NULL,
  task_id text,
  status text NOT NULL DEFAULT 'draft',
  payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  reviewed_at timestamptz,
  CONSTRAINT evidence_status_valid CHECK (
    status IN ('draft', 'reviewing', 'reviewed')
  ),
  CONSTRAINT evidence_reviewed_at_valid CHECK (
    (status = 'reviewed' AND reviewed_at IS NOT NULL)
    OR (status <> 'reviewed')
  )
);

COMMENT ON TABLE evidence IS 'M0 rebuildable projection for submitted work. Full business fields are introduced in the stage-3 evidence expansion.';
COMMENT ON COLUMN evidence.payload IS 'Minimal JSON projection from EvidenceSubmitted and ReviewCompleted while M0 stabilizes the execution kernel.';

CREATE INDEX evidence_user_created_at_idx ON evidence (user_id, created_at DESC);
CREATE INDEX evidence_user_status_created_at_idx ON evidence (user_id, status, created_at DESC);
CREATE INDEX evidence_task_id_idx ON evidence (task_id) WHERE task_id IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS evidence;
DROP TABLE IF EXISTS tool_calls;
DROP TABLE IF EXISTS run_message_chunks;
DROP TABLE IF EXISTS run_messages;
DROP TABLE IF EXISTS llm_ledger;
DROP TABLE IF EXISTS conversations;
DROP TABLE IF EXISTS runs;

DROP INDEX IF EXISTS events_conversation_created_at_idx;
DROP INDEX IF EXISTS events_conversation_seq_idx;

ALTER TABLE events
  DROP COLUMN IF EXISTS conversation_id;

ALTER TABLE events
  DROP COLUMN IF EXISTS tenant_id;

ALTER TABLE jobs
  DROP COLUMN IF EXISTS store_epoch;

ALTER TABLE event_cursors
  DROP CONSTRAINT event_cursors_next_seq_positive;

ALTER TABLE event_cursors
  ALTER COLUMN next_seq SET DEFAULT 0;

UPDATE event_cursors SET next_seq = next_seq - 1;

ALTER TABLE event_cursors
  RENAME COLUMN next_seq TO current_seq;

ALTER TABLE event_cursors
  ADD CONSTRAINT event_cursors_current_seq_nonnegative CHECK (current_seq >= 0);
