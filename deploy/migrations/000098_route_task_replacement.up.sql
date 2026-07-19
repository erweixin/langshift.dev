DROP INDEX IF EXISTS product.daily_task_generations_one_live_daily;

-- Generation idempotency follows the immutable causal input. A corrected
-- route may generate a replacement on the same day, while duplicate delivery
-- of that exact Mission/Route/Focus request still converges to one generation.
CREATE UNIQUE INDEX daily_task_generations_one_live_daily
  ON product.daily_task_generations(
    tenant_id,user_id,mission_id,route_revision_id,focus_version,scheduled_for
  )
  WHERE status IN ('generating','succeeded');

DROP INDEX IF EXISTS product.daily_tasks_one_daily_commitment;

-- A route correction may replace an unsubmitted task on the same local day.
-- The superseded task remains as an audited skipped fact, while only live and
-- completed work retains the one-commitment slot.
CREATE UNIQUE INDEX daily_tasks_one_daily_commitment
  ON product.daily_tasks(tenant_id,user_id,scheduled_for)
  WHERE status NOT IN ('rescheduled','skipped');
