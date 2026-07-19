DO $$
DECLARE
  task_predicate text;
  generation_definition text;
BEGIN
  SELECT pg_get_expr(index.indpred,index.indrelid)
    INTO task_predicate
  FROM pg_index index
  JOIN pg_class relation ON relation.oid=index.indexrelid
  JOIN pg_namespace namespace ON namespace.oid=relation.relnamespace
  WHERE namespace.nspname='product'
    AND relation.relname='daily_tasks_one_daily_commitment';

  SELECT pg_get_indexdef(index.indexrelid)
    INTO generation_definition
  FROM pg_index index
  JOIN pg_class relation ON relation.oid=index.indexrelid
  JOIN pg_namespace namespace ON namespace.oid=relation.relnamespace
  WHERE namespace.nspname='product'
    AND relation.relname='daily_task_generations_one_live_daily';

  IF task_predicate IS NULL
    OR position('rescheduled' IN task_predicate)=0
    OR position('skipped' IN task_predicate)=0
    OR generation_definition IS NULL
    OR position('mission_id' IN generation_definition)=0
    OR position('route_revision_id' IN generation_definition)=0
    OR position('focus_version' IN generation_definition)=0
    OR position('scheduled_for' IN generation_definition)=0 THEN
    RAISE EXCEPTION 'route task replacement commitment boundary is incomplete';
  END IF;
END
$$;

SELECT json_build_object(
  'status', 'passed',
  'schema_version', 98,
  'contract', 'route_task_replacement'
) AS current_schema_verification;
