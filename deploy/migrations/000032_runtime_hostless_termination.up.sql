CREATE OR REPLACE FUNCTION agent.validate_runtime_allocation_session() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE allocation_status text; allocation_session_version bigint; allocation_event uuid;
BEGIN
  IF TG_TABLE_NAME='runtime_allocations' THEN
    allocation_status:=NEW.status; allocation_session_version:=NEW.session_version; allocation_event:=NEW.session_event_id;
    IF NOT EXISTS (
      SELECT 1 FROM agent.runtime_sessions s
      WHERE s.tenant_id=NEW.tenant_id AND s.id=NEW.session_id AND s.user_id=NEW.user_id
        AND s.version=allocation_session_version AND s.last_event_id=allocation_event
        AND ((allocation_status='provisioning' AND s.status='provisioning'
              AND s.provision_lease_hash=NEW.lease_token_hash AND s.provision_lease_expires_at=NEW.lease_expires_at)
          OR (allocation_status='active' AND s.status IN ('ready','running','idle'))
          OR (allocation_status='releasing' AND s.status='termination_requested')
          OR (allocation_status='released' AND s.status='terminated')
          OR (allocation_status='lost' AND s.status='failed'))
    ) THEN RAISE EXCEPTION 'runtime allocation lacks matching session state'; END IF;
  ELSE
    IF NEW.isolation_kind='firecracker' AND NEW.host_id IS NOT NULL AND NOT EXISTS (
      SELECT 1 FROM agent.runtime_allocations a
      WHERE a.tenant_id=NEW.tenant_id AND a.session_id=NEW.id AND a.user_id=NEW.user_id
        AND a.session_version=NEW.version AND a.session_event_id=NEW.last_event_id
        AND ((NEW.status='provisioning' AND a.status='provisioning')
          OR (NEW.status IN ('ready','running','idle') AND a.status='active')
          OR (NEW.status='termination_requested' AND a.status='releasing')
          OR (NEW.status='terminated' AND a.status='released')
          OR (NEW.status='failed' AND a.status='lost'))
    ) THEN RAISE EXCEPTION 'firecracker session lacks matching host allocation';
    ELSIF NEW.isolation_kind='firecracker' AND NEW.host_id IS NULL
      AND NEW.status NOT IN ('requested','termination_requested','terminated','failed')
    THEN RAISE EXCEPTION 'hostless firecracker session has invalid state'; END IF;
  END IF;
  RETURN NULL;
END
$$;
