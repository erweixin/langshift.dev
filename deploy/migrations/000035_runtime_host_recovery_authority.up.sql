CREATE FUNCTION agent.runtime_lock_owned_machine(
  p_store_epoch uuid,p_host_id text,p_control_token_hash bytea,p_tenant_id uuid,p_session_id uuid,
  p_allocation_id uuid,p_provision_attempt_id uuid,p_machine_id text,p_guest_cid bigint
) RETURNS TABLE(
  tenant_id text,session_id text,allocation_id text,provision_attempt_id text,host_id text,machine_id text,
  guest_cid bigint,session_version bigint,session_status text,allocation_version bigint,allocation_status text,
  provision_fence bigint,provision_lease_expires_at timestamptz,idle_deadline timestamptz,
  execution_deadline timestamptz,kill_deadline timestamptz
)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,agent SET row_security=off AS $$
BEGIN
  IF p_store_epoch IS NULL OR NULLIF(p_host_id,'') IS NULL OR octet_length(p_control_token_hash)<>32
    OR p_tenant_id IS NULL OR p_session_id IS NULL OR p_allocation_id IS NULL OR p_provision_attempt_id IS NULL
    OR p_machine_id !~ '^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$' OR p_guest_cid NOT BETWEEN 3 AND 4294967295
  THEN RAISE EXCEPTION 'invalid owned runtime recovery identity' USING ERRCODE='22023'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM agent.runtime_hosts h WHERE h.host_id=p_host_id
      AND h.control_token_hash=p_control_token_hash AND h.status IN ('recovering','active','draining','unhealthy')
  ) THEN RAISE EXCEPTION 'runtime host recovery authority rejected' USING ERRCODE='42501'; END IF;
  RETURN QUERY SELECT s.tenant_id::text,s.id::text,a.id::text,s.provision_attempt_id::text,s.host_id,s.machine_id,
    s.guest_cid,s.version,s.status,a.version,a.status,s.provision_fence,s.provision_lease_expires_at,
    s.idle_deadline,s.execution_deadline,s.kill_deadline
  FROM agent.runtime_sessions s
  JOIN agent.runtime_allocations a ON a.tenant_id=s.tenant_id AND a.session_id=s.id
  JOIN agent.events e ON e.tenant_id=s.tenant_id AND e.id=s.last_event_id AND e.store_epoch=p_store_epoch
  WHERE s.tenant_id=p_tenant_id AND s.id=p_session_id AND a.id=p_allocation_id
    AND s.host_id=p_host_id AND a.host_id=p_host_id AND s.machine_id=p_machine_id AND a.machine_id=p_machine_id
    AND s.guest_cid=p_guest_cid AND a.guest_cid=p_guest_cid
    AND s.provision_attempt_id=p_provision_attempt_id AND a.provision_attempt_id=p_provision_attempt_id
    AND s.provision_fence=a.provision_fence
    AND s.status IN ('provisioning','ready','running','idle','termination_requested','terminated','failed')
    AND a.status IN ('provisioning','active','releasing','released','lost')
  FOR UPDATE OF s,a;
  IF NOT FOUND THEN RAISE EXCEPTION 'owned runtime machine does not match durable state' USING ERRCODE='40001'; END IF;
END
$$;

CREATE FUNCTION agent.runtime_lock_due_session(
  p_store_epoch uuid,p_tenant_id uuid,p_session_id uuid,p_expected_version bigint,p_now timestamptz
) RETURNS TABLE(
  tenant_id text,session_id text,allocation_id text,provision_attempt_id text,host_id text,machine_id text,
  guest_cid bigint,session_version bigint,session_status text,allocation_version bigint,allocation_status text,
  provision_fence bigint,provision_lease_expires_at timestamptz,idle_deadline timestamptz,
  execution_deadline timestamptz,kill_deadline timestamptz
)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,agent SET row_security=off AS $$
BEGIN
  IF p_store_epoch IS NULL OR p_tenant_id IS NULL OR p_session_id IS NULL OR p_expected_version<2 OR p_now IS NULL
  THEN RAISE EXCEPTION 'invalid runtime deadline recovery identity' USING ERRCODE='22023'; END IF;
  RETURN QUERY SELECT s.tenant_id::text,s.id::text,a.id::text,s.provision_attempt_id::text,s.host_id,s.machine_id,
    s.guest_cid,s.version,s.status,a.version,a.status,s.provision_fence,s.provision_lease_expires_at,
    s.idle_deadline,s.execution_deadline,s.kill_deadline
  FROM agent.runtime_sessions s
  JOIN agent.runtime_allocations a ON a.tenant_id=s.tenant_id AND a.session_id=s.id
  JOIN agent.events e ON e.tenant_id=s.tenant_id AND e.id=s.last_event_id AND e.store_epoch=p_store_epoch
  WHERE s.tenant_id=p_tenant_id AND s.id=p_session_id
    AND ((s.version=p_expected_version AND (
      (s.status='provisioning' AND s.provision_lease_expires_at<=p_now)
      OR (s.status IN ('ready','idle') AND s.idle_deadline<=p_now)
      OR (s.status='running' AND s.execution_deadline<=p_now)
    )) OR (s.version=p_expected_version+1 AND s.status='termination_requested'))
    AND ((s.status='provisioning' AND a.status='provisioning')
      OR (s.status IN ('ready','running','idle') AND a.status='active')
      OR (s.status='termination_requested' AND a.status='releasing'))
    AND a.session_version=s.version AND a.session_event_id=s.last_event_id
  FOR UPDATE OF s,a;
  IF NOT FOUND THEN RAISE EXCEPTION 'runtime session is not due for recovery' USING ERRCODE='40001'; END IF;
END
$$;

CREATE FUNCTION agent.runtime_list_host_machines(
  p_store_epoch uuid,p_host_id text,p_control_token_hash bytea,p_after_session uuid,p_limit integer
) RETURNS TABLE(
  tenant_id text,session_id text,allocation_id text,provision_attempt_id text,machine_id text,guest_cid bigint,
  session_version bigint,session_status text,allocation_status text,provision_fence bigint
)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,agent SET row_security=off AS $$
BEGIN
  IF p_store_epoch IS NULL OR NULLIF(p_host_id,'') IS NULL OR octet_length(p_control_token_hash)<>32
    OR p_limit IS NULL OR p_limit<1 OR p_limit>5000
  THEN RAISE EXCEPTION 'invalid runtime host inventory request' USING ERRCODE='22023'; END IF;
  IF NOT EXISTS (
    SELECT 1 FROM agent.runtime_hosts h WHERE h.host_id=p_host_id
      AND h.control_token_hash=p_control_token_hash AND h.status IN ('recovering','active','draining','unhealthy')
  ) THEN RAISE EXCEPTION 'runtime host inventory authority rejected' USING ERRCODE='42501'; END IF;
  RETURN QUERY SELECT s.tenant_id::text,s.id::text,a.id::text,s.provision_attempt_id::text,s.machine_id,s.guest_cid,
    s.version,s.status,a.status,s.provision_fence
  FROM agent.runtime_sessions s
  JOIN agent.runtime_allocations a ON a.tenant_id=s.tenant_id AND a.session_id=s.id AND a.host_id=p_host_id
  JOIN agent.events e ON e.tenant_id=s.tenant_id AND e.id=s.last_event_id AND e.store_epoch=p_store_epoch
  WHERE s.host_id=p_host_id AND s.id>COALESCE(p_after_session,'00000000-0000-0000-0000-000000000000'::uuid)
    AND s.status IN ('provisioning','ready','running','idle','termination_requested')
    AND a.status IN ('provisioning','active','releasing')
  ORDER BY s.id LIMIT p_limit;
END
$$;

REVOKE ALL ON FUNCTION agent.runtime_lock_owned_machine(uuid,text,bytea,uuid,uuid,uuid,uuid,text,bigint) FROM PUBLIC;
REVOKE ALL ON FUNCTION agent.runtime_lock_due_session(uuid,uuid,uuid,bigint,timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION agent.runtime_list_host_machines(uuid,text,bytea,uuid,integer) FROM PUBLIC;
