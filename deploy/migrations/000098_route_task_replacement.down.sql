DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM product.daily_task_generations
    WHERE status IN ('generating','succeeded')
    GROUP BY tenant_id,user_id,scheduled_for
    HAVING count(*)>1
  ) THEN
    RAISE EXCEPTION 'cannot restore the pre-98 daily generation index while replacement generations exist';
  END IF;
  IF EXISTS (
    SELECT 1
    FROM product.daily_tasks
    WHERE status<>'rescheduled'
    GROUP BY tenant_id,user_id,scheduled_for
    HAVING count(*)>1
  ) THEN
    RAISE EXCEPTION 'cannot restore the pre-98 daily commitment index while replacement tasks exist';
  END IF;
END
$$;

DROP INDEX IF EXISTS product.daily_task_generations_one_live_daily;

CREATE UNIQUE INDEX daily_task_generations_one_live_daily
  ON product.daily_task_generations(tenant_id,user_id,scheduled_for)
  WHERE status IN ('generating','succeeded');

DROP INDEX IF EXISTS product.daily_tasks_one_daily_commitment;

CREATE UNIQUE INDEX daily_tasks_one_daily_commitment
  ON product.daily_tasks(tenant_id,user_id,scheduled_for)
  WHERE status<>'rescheduled';
