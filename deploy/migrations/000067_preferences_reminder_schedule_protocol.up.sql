DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM product.reminder_schedules) OR EXISTS (SELECT 1 FROM product.reminder_deliveries) THEN
    RAISE EXCEPTION 'reminder schedule protocol requires pre-GA reminder tables to be empty';
  END IF;
END $$;

ALTER TABLE product.user_preferences
  ADD CONSTRAINT user_preferences_contract CHECK (
    version>0
    AND locale IN ('en','zh-CN')
    AND char_length(timezone) BETWEEN 1 AND 128
    AND jsonb_typeof(coach_preferences)='object'
    AND (coach_preferences->>'schema_version')::integer=1
    AND coach_preferences->>'difficulty' IN ('easier','standard','harder')
    AND (coach_preferences->>'available_minutes')::integer BETWEEN 5 AND 480
    AND coach_preferences->>'tone' IN ('encouraging','direct','socratic')
    AND coach_preferences->>'explanation_depth' IN ('concise','balanced','deep')
    AND coach_preferences - ARRAY['schema_version','difficulty','available_minutes','tone','explanation_depth'] = '{}'::jsonb
  ) NOT VALID;

ALTER TABLE product.reminder_schedules
  ADD COLUMN schedule_kind text,
  ADD COLUMN local_time time without time zone,
  ADD COLUMN weekdays smallint[],
  ADD COLUMN channel text;

ALTER TABLE product.reminder_schedules
  ALTER COLUMN schedule_kind SET NOT NULL,
  ALTER COLUMN local_time SET NOT NULL,
  ALTER COLUMN weekdays SET NOT NULL,
  ALTER COLUMN channel SET NOT NULL,
  ADD CONSTRAINT reminder_schedules_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT reminder_schedules_contract CHECK (
    schedule_kind='daily_practice'
    AND char_length(timezone) BETWEEN 1 AND 128
    AND cardinality(weekdays) BETWEEN 1 AND 7
    AND weekdays <@ ARRAY[1,2,3,4,5,6,7]::smallint[]
    AND channel IN ('email','push','in_app')
    AND jsonb_typeof(local_schedule)='object'
    AND local_schedule->>'schema_version'='1'
    AND local_schedule->>'dst_policy'='next_valid_wall_time_earliest_duplicate'
  ),
  ADD CONSTRAINT reminder_schedules_lifecycle_contract CHECK (
    (status='active' AND next_occurrence_at IS NOT NULL AND cancelled_at IS NULL)
    OR (status='paused' AND next_occurrence_at IS NULL AND cancelled_at IS NULL)
    OR (status='cancelled' AND next_occurrence_at IS NULL AND cancelled_at IS NOT NULL)
    OR (status='completed' AND next_occurrence_at IS NULL AND cancelled_at IS NULL)
  );

CREATE UNIQUE INDEX reminder_schedules_one_live_kind
  ON product.reminder_schedules(tenant_id,user_id,schedule_kind)
  WHERE status IN ('active','paused');

ALTER TABLE product.reminder_deliveries
  ADD CONSTRAINT reminder_deliveries_schedule_fk FOREIGN KEY (tenant_id,schedule_id)
    REFERENCES product.reminder_schedules(tenant_id,id) ON DELETE CASCADE,
  ADD CONSTRAINT reminder_deliveries_contract CHECK (
    delivery_key ~ '^[0-9a-f]{64}$'
    AND status IN ('pending','delivering','delivered','failed','cancelled')
    AND attempt_count BETWEEN 0 AND 20
    AND ((status='delivered' AND delivered_at IS NOT NULL AND last_error_code IS NULL)
      OR (status<>'delivered' AND delivered_at IS NULL))
  );

CREATE FUNCTION agent.list_due_reminder_schedule_tenants(p_after_tenant uuid,p_limit integer)
RETURNS TABLE(tenant_id uuid)
LANGUAGE sql SECURITY DEFINER
SET search_path=pg_catalog,product SET row_security=off
AS $$
  SELECT DISTINCT s.tenant_id FROM product.reminder_schedules s
  WHERE s.status='active' AND s.next_occurrence_at<=statement_timestamp()
    AND (p_after_tenant IS NULL OR s.tenant_id>p_after_tenant)
  ORDER BY s.tenant_id LIMIT LEAST(GREATEST(p_limit,1),5000)
$$;
REVOKE ALL ON FUNCTION agent.list_due_reminder_schedule_tenants(uuid,integer) FROM PUBLIC;

DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    GRANT SELECT,INSERT,UPDATE ON product.user_preferences,product.reminder_schedules,product.reminder_deliveries TO lites_product_service;
    GRANT EXECUTE ON FUNCTION agent.list_due_reminder_schedule_tenants(uuid,integer) TO lites_product_service;
  END IF;
END $$;
