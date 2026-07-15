CREATE OR REPLACE FUNCTION agent.enforce_runtime_host_lifecycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'runtime host deletion is forbidden'; END IF;
  IF OLD.status='retired' THEN RAISE EXCEPTION 'retired runtime host mutation is forbidden'; END IF;
  IF NEW.host_id IS DISTINCT FROM OLD.host_id OR NEW.pool_key IS DISTINCT FROM OLD.pool_key
    OR NEW.architecture IS DISTINCT FROM OLD.architecture OR NEW.availability_zone IS DISTINCT FROM OLD.availability_zone
    OR NEW.firecracker_version IS DISTINCT FROM OLD.firecracker_version
    OR NEW.kernel_catalog_hash IS DISTINCT FROM OLD.kernel_catalog_hash
    OR NEW.rootfs_catalog_hash IS DISTINCT FROM OLD.rootfs_catalog_hash
    OR NEW.scratch_template_digest IS DISTINCT FROM OLD.scratch_template_digest
    OR NEW.control_token_hash IS DISTINCT FROM OLD.control_token_hash
    OR NEW.capacity_vcpu IS DISTINCT FROM OLD.capacity_vcpu OR NEW.capacity_memory_mib IS DISTINCT FROM OLD.capacity_memory_mib
    OR NEW.capacity_disk_mib IS DISTINCT FROM OLD.capacity_disk_mib OR NEW.capacity_sessions IS DISTINCT FROM OLD.capacity_sessions
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'immutable runtime host scope mutation is forbidden';
  END IF;
  IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at OR NEW.heartbeat_at<OLD.heartbeat_at
    OR NEW.heartbeat_deadline<=NEW.heartbeat_at THEN
    RAISE EXCEPTION 'runtime host version or heartbeat is invalid';
  END IF;
  IF NEW.status<>OLD.status AND NOT (
    (OLD.status='recovering' AND NEW.status IN ('active','unhealthy','draining'))
    OR (OLD.status='active' AND NEW.status IN ('recovering','draining','unhealthy'))
    OR (OLD.status='unhealthy' AND NEW.status IN ('recovering','draining'))
    OR (OLD.status='draining' AND NEW.status IN ('recovering','retired'))
  ) THEN RAISE EXCEPTION 'invalid runtime host transition % -> %',OLD.status,NEW.status; END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION agent.runtime_register_host(
  p_host_id text,p_pool_key text,p_architecture text,p_availability_zone text,
  p_kernel_catalog_hash text,p_rootfs_catalog_hash text,p_scratch_template_digest text,
  p_control_token_hash bytea,
  p_capacity_vcpu integer,p_capacity_memory_mib integer,p_capacity_disk_mib integer,p_capacity_sessions integer,
  p_now timestamptz,p_heartbeat_deadline timestamptz
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,agent SET row_security=off AS $$
DECLARE result_version bigint;
BEGIN
  INSERT INTO agent.runtime_hosts(
    host_id,version,pool_key,status,architecture,availability_zone,firecracker_version,
    kernel_catalog_hash,rootfs_catalog_hash,scratch_template_digest,control_token_hash,
    capacity_vcpu,capacity_memory_mib,capacity_disk_mib,capacity_sessions,
    heartbeat_at,heartbeat_deadline,created_at,updated_at
  ) VALUES(
    p_host_id,1,p_pool_key,'recovering',p_architecture,p_availability_zone,'1.15.1',
    p_kernel_catalog_hash,p_rootfs_catalog_hash,p_scratch_template_digest,p_control_token_hash,
    p_capacity_vcpu,p_capacity_memory_mib,p_capacity_disk_mib,p_capacity_sessions,
    p_now,p_heartbeat_deadline,p_now,p_now
  )
  ON CONFLICT(host_id) DO UPDATE SET
    version=agent.runtime_hosts.version+1,status='recovering',heartbeat_at=p_now,
    heartbeat_deadline=p_heartbeat_deadline,updated_at=p_now
  WHERE agent.runtime_hosts.pool_key=EXCLUDED.pool_key
    AND agent.runtime_hosts.architecture=EXCLUDED.architecture
    AND agent.runtime_hosts.availability_zone=EXCLUDED.availability_zone
    AND agent.runtime_hosts.firecracker_version=EXCLUDED.firecracker_version
    AND agent.runtime_hosts.kernel_catalog_hash=EXCLUDED.kernel_catalog_hash
    AND agent.runtime_hosts.rootfs_catalog_hash=EXCLUDED.rootfs_catalog_hash
    AND agent.runtime_hosts.scratch_template_digest=EXCLUDED.scratch_template_digest
    AND agent.runtime_hosts.control_token_hash=EXCLUDED.control_token_hash
    AND agent.runtime_hosts.capacity_vcpu=EXCLUDED.capacity_vcpu
    AND agent.runtime_hosts.capacity_memory_mib=EXCLUDED.capacity_memory_mib
    AND agent.runtime_hosts.capacity_disk_mib=EXCLUDED.capacity_disk_mib
    AND agent.runtime_hosts.capacity_sessions=EXCLUDED.capacity_sessions
    AND agent.runtime_hosts.status IN ('recovering','active','draining','unhealthy')
  RETURNING version INTO result_version;
  IF result_version IS NULL THEN RAISE EXCEPTION 'runtime host registration conflict' USING ERRCODE='40001'; END IF;
  RETURN result_version;
END
$$;

REVOKE ALL ON FUNCTION agent.runtime_register_host(text,text,text,text,text,text,text,bytea,integer,integer,integer,integer,timestamptz,timestamptz) FROM PUBLIC;
