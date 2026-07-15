CREATE FUNCTION agent.runtime_lock_cancelled_run_session(
  p_store_epoch uuid,p_tenant_id uuid,p_run_id uuid,p_cancellation_id uuid,
  p_session_id uuid,p_expected_version bigint,p_now timestamptz
) RETURNS TABLE(
  tenant_id text,session_id text,allocation_id text,provision_attempt_id text,host_id text,machine_id text,
  guest_cid bigint,session_version bigint,session_status text,allocation_version bigint,allocation_status text,
  provision_fence bigint,provision_lease_expires_at timestamptz,idle_deadline timestamptz,
  execution_deadline timestamptz,kill_deadline timestamptz
)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,agent SET row_security=off AS $$
BEGIN
  IF p_store_epoch IS NULL OR p_tenant_id IS NULL OR p_run_id IS NULL OR p_cancellation_id IS NULL
    OR p_session_id IS NULL OR p_expected_version<2 OR p_now IS NULL
  THEN RAISE EXCEPTION 'invalid runtime run-cancellation authority' USING ERRCODE='22023'; END IF;

  RETURN QUERY SELECT s.tenant_id::text,s.id::text,a.id::text,s.provision_attempt_id::text,s.host_id,s.machine_id,
    s.guest_cid,s.version,s.status,a.version,a.status,s.provision_fence,s.provision_lease_expires_at,
    s.idle_deadline,s.execution_deadline,s.kill_deadline
  FROM agent.runtime_sessions s
  JOIN agent.runtime_allocations a ON a.tenant_id=s.tenant_id AND a.session_id=s.id
  JOIN agent.runs r ON r.tenant_id=s.tenant_id AND r.id=s.run_id
  JOIN agent.run_cancellations c ON c.tenant_id=r.tenant_id AND c.id=r.active_cancellation_id
  JOIN agent.events ce ON ce.tenant_id=c.tenant_id AND ce.id=c.request_event_id
    AND ce.aggregate_kind='run_cancellation' AND ce.aggregate_id=c.id
    AND ce.event_type='RunCancellationRequested' AND ce.aggregate_version=1
    AND ce.store_epoch=p_store_epoch
  JOIN agent.events se ON se.tenant_id=s.tenant_id AND se.id=s.last_event_id AND se.store_epoch=p_store_epoch
  WHERE s.tenant_id=p_tenant_id AND s.id=p_session_id AND s.run_id=p_run_id
    AND c.id=p_cancellation_id AND c.store_epoch=p_store_epoch AND c.status='terminating'
    AND r.cancel_requested_at IS NOT NULL AND r.cancel_requested_at<=p_now
    AND ((s.version=p_expected_version AND s.status IN ('provisioning','ready','running','idle'))
      OR (s.version=p_expected_version+1 AND s.status='termination_requested'))
    AND ((s.status='provisioning' AND a.status='provisioning')
      OR (s.status IN ('ready','running','idle') AND a.status='active')
      OR (s.status='termination_requested' AND a.status='releasing'))
    AND a.session_version=s.version AND a.session_event_id=s.last_event_id
  FOR UPDATE OF s,a;
  IF NOT FOUND THEN RAISE EXCEPTION 'runtime session is not authorized by run cancellation' USING ERRCODE='40001'; END IF;
END
$$;

REVOKE ALL ON FUNCTION agent.runtime_lock_cancelled_run_session(uuid,uuid,uuid,uuid,uuid,bigint,timestamptz) FROM PUBLIC;
