CREATE TABLE agent.runtime_hosts (
  host_id text PRIMARY KEY,
  version bigint NOT NULL,
  pool_key text NOT NULL,
  status text NOT NULL,
  architecture text NOT NULL,
  availability_zone text NOT NULL,
  firecracker_version text NOT NULL,
  kernel_catalog_hash text NOT NULL,
  rootfs_catalog_hash text NOT NULL,
  scratch_template_digest text NOT NULL,
  control_token_hash bytea NOT NULL,
  capacity_vcpu integer NOT NULL,
  capacity_memory_mib integer NOT NULL,
  capacity_disk_mib integer NOT NULL,
  capacity_sessions integer NOT NULL,
  allocated_vcpu integer NOT NULL DEFAULT 0,
  allocated_memory_mib integer NOT NULL DEFAULT 0,
  allocated_disk_mib integer NOT NULL DEFAULT 0,
  allocated_sessions integer NOT NULL DEFAULT 0,
  heartbeat_at timestamptz NOT NULL,
  heartbeat_deadline timestamptz NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  CONSTRAINT runtime_hosts_identity_contract CHECK (
    host_id ~ '^[a-z0-9][a-z0-9.-]{2,127}$' AND pool_key ~ '^[a-z][a-z0-9._:-]{2,127}$'
    AND architecture IN ('x86_64','aarch64') AND availability_zone ~ '^[a-z0-9-]{2,63}$'
    AND firecracker_version='1.15.1'
    AND kernel_catalog_hash ~ '^sha256:[0-9a-f]{64}$'
    AND rootfs_catalog_hash ~ '^sha256:[0-9a-f]{64}$'
    AND scratch_template_digest ~ '^sha256:[0-9a-f]{64}$'
    AND octet_length(control_token_hash)=32
  ),
  CONSTRAINT runtime_hosts_capacity_contract CHECK (
    version>0 AND status IN ('recovering','active','draining','unhealthy','retired')
    AND capacity_vcpu BETWEEN 1 AND 4096 AND capacity_memory_mib BETWEEN 128 AND 16777216
    AND capacity_disk_mib BETWEEN 64 AND 1073741824 AND capacity_sessions BETWEEN 1 AND 10000
    AND allocated_vcpu BETWEEN 0 AND capacity_vcpu
    AND allocated_memory_mib BETWEEN 0 AND capacity_memory_mib
    AND allocated_disk_mib BETWEEN 0 AND capacity_disk_mib
    AND allocated_sessions BETWEEN 0 AND capacity_sessions
    AND heartbeat_deadline>heartbeat_at AND updated_at>=created_at
    AND (status<>'retired' OR allocated_sessions=0)
  )
);

ALTER TABLE agent.runtime_sessions
  ADD CONSTRAINT runtime_sessions_allocation_binding_unique UNIQUE (
    tenant_id,id,host_id,machine_id,guest_cid,provision_attempt_id,provision_fence
  );

CREATE TABLE agent.runtime_allocations (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  session_id uuid NOT NULL,
  host_id text NOT NULL,
  machine_id text NOT NULL,
  guest_cid bigint NOT NULL,
  version bigint NOT NULL,
  status text NOT NULL,
  session_version bigint NOT NULL,
  session_event_id uuid NOT NULL,
  provision_attempt_id uuid NOT NULL,
  provision_fence bigint NOT NULL,
  vcpu_count integer NOT NULL,
  memory_mib integer NOT NULL,
  disk_mib integer NOT NULL,
  lease_token_hash bytea,
  lease_expires_at timestamptz,
  terminal_reason text,
  terminal_receipt_hash text,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  released_at timestamptz,
  CONSTRAINT runtime_allocations_tenant_id_id_unique UNIQUE (tenant_id,id),
  CONSTRAINT runtime_allocations_session_unique UNIQUE (tenant_id,session_id),
  CONSTRAINT runtime_allocations_scope_contract CHECK (
    version>0 AND status IN ('provisioning','active','releasing','released','lost')
    AND machine_id ~ '^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$' AND guest_cid BETWEEN 3 AND 4294967295
    AND session_version>0 AND provision_fence>0
    AND vcpu_count BETWEEN 1 AND 32 AND memory_mib BETWEEN 128 AND 32768 AND memory_mib%2=0
    AND disk_mib BETWEEN 64 AND 262144 AND updated_at>=created_at
  ),
  CONSTRAINT runtime_allocations_lease_contract CHECK (
    (status IN ('provisioning','active','releasing') AND octet_length(lease_token_hash)=32 AND lease_expires_at>updated_at
      AND terminal_reason IS NULL AND terminal_receipt_hash IS NULL AND released_at IS NULL)
    OR (status IN ('released','lost') AND lease_token_hash IS NULL AND lease_expires_at IS NULL
      AND NULLIF(terminal_reason,'') IS NOT NULL AND terminal_receipt_hash ~ '^[0-9a-f]{64}$' AND released_at IS NOT NULL)
  ),
  CONSTRAINT runtime_allocations_host_fk FOREIGN KEY (host_id)
    REFERENCES agent.runtime_hosts(host_id) ON DELETE RESTRICT,
  CONSTRAINT runtime_allocations_session_fk FOREIGN KEY (
    tenant_id,session_id,host_id,machine_id,guest_cid,provision_attempt_id,provision_fence
  ) REFERENCES agent.runtime_sessions(
    tenant_id,id,host_id,machine_id,guest_cid,provision_attempt_id,provision_fence
  ) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT runtime_allocations_event_fk FOREIGN KEY (session_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);

CREATE UNIQUE INDEX runtime_allocations_host_machine_active_unique
  ON agent.runtime_allocations(host_id,machine_id) WHERE status IN ('provisioning','active','releasing');
CREATE UNIQUE INDEX runtime_allocations_host_cid_active_unique
  ON agent.runtime_allocations(host_id,guest_cid) WHERE status IN ('provisioning','active','releasing');
CREATE INDEX runtime_allocations_recovery_idx
  ON agent.runtime_allocations(host_id,status,lease_expires_at,id) WHERE status IN ('provisioning','active','releasing');
CREATE INDEX runtime_hosts_placement_idx
  ON agent.runtime_hosts(pool_key,status,heartbeat_deadline,allocated_sessions,host_id) WHERE status='active';

CREATE FUNCTION agent.enforce_runtime_host_lifecycle() RETURNS trigger
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
    OR (OLD.status='draining' AND NEW.status='retired')
  ) THEN RAISE EXCEPTION 'invalid runtime host transition % -> %',OLD.status,NEW.status; END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER runtime_host_lifecycle BEFORE UPDATE OR DELETE ON agent.runtime_hosts
  FOR EACH ROW EXECUTE FUNCTION agent.enforce_runtime_host_lifecycle();

CREATE FUNCTION agent.enforce_runtime_allocation_lifecycle() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,agent SET row_security=off AS $$
DECLARE changed integer;
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'runtime allocation deletion is forbidden'; END IF;
  IF TG_OP='INSERT' THEN
    IF NEW.version<>1 OR NEW.status<>'provisioning' OR NEW.session_version<2 OR NEW.provision_fence<>1 THEN
      RAISE EXCEPTION 'invalid initial runtime allocation';
    END IF;
    UPDATE agent.runtime_hosts SET
      version=version+1,allocated_vcpu=allocated_vcpu+NEW.vcpu_count,
      allocated_memory_mib=allocated_memory_mib+NEW.memory_mib,
      allocated_disk_mib=allocated_disk_mib+NEW.disk_mib,allocated_sessions=allocated_sessions+1,
      updated_at=NEW.created_at
    WHERE host_id=NEW.host_id AND status='active' AND heartbeat_deadline>NEW.created_at
      AND allocated_vcpu+NEW.vcpu_count<=capacity_vcpu
      AND allocated_memory_mib+NEW.memory_mib<=capacity_memory_mib
      AND allocated_disk_mib+NEW.disk_mib<=capacity_disk_mib
      AND allocated_sessions+1<=capacity_sessions;
    GET DIAGNOSTICS changed=ROW_COUNT;
    IF changed<>1 THEN RAISE EXCEPTION 'runtime host capacity unavailable' USING ERRCODE='40001'; END IF;
    RETURN NEW;
  END IF;
  IF OLD.status IN ('released','lost') THEN RAISE EXCEPTION 'terminal runtime allocation mutation is forbidden'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.user_id IS DISTINCT FROM OLD.user_id OR NEW.session_id IS DISTINCT FROM OLD.session_id
    OR NEW.host_id IS DISTINCT FROM OLD.host_id OR NEW.machine_id IS DISTINCT FROM OLD.machine_id
    OR NEW.guest_cid IS DISTINCT FROM OLD.guest_cid OR NEW.provision_attempt_id IS DISTINCT FROM OLD.provision_attempt_id
    OR NEW.provision_fence IS DISTINCT FROM OLD.provision_fence OR NEW.vcpu_count IS DISTINCT FROM OLD.vcpu_count
    OR NEW.memory_mib IS DISTINCT FROM OLD.memory_mib OR NEW.disk_mib IS DISTINCT FROM OLD.disk_mib
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'immutable runtime allocation scope mutation is forbidden';
  END IF;
  IF NEW.status NOT IN ('released','lost') AND NEW.lease_token_hash IS DISTINCT FROM OLD.lease_token_hash THEN
    RAISE EXCEPTION 'runtime allocation lease identity mutation is forbidden';
  END IF;
  IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN
    RAISE EXCEPTION 'runtime allocation version must advance exactly once';
  END IF;
  IF NEW.status=OLD.status THEN
    IF NEW.session_version=OLD.session_version AND NEW.session_event_id=OLD.session_event_id THEN
      IF NEW.lease_expires_at<=OLD.lease_expires_at THEN RAISE EXCEPTION 'runtime allocation heartbeat must extend lease'; END IF;
    ELSIF NEW.session_version<>OLD.session_version+1 OR NEW.session_event_id=OLD.session_event_id
      OR NEW.lease_expires_at<OLD.lease_expires_at THEN
      RAISE EXCEPTION 'runtime allocation session synchronization is invalid';
    END IF;
    RETURN NEW;
  END IF;
  IF NEW.session_version<>OLD.session_version+1 OR NEW.session_event_id=OLD.session_event_id THEN
    RAISE EXCEPTION 'runtime allocation transition must advance session evidence';
  END IF;
  IF NOT (
    (OLD.status='provisioning' AND NEW.status IN ('active','releasing','lost'))
    OR (OLD.status='active' AND NEW.status IN ('releasing','lost'))
    OR (OLD.status='releasing' AND NEW.status IN ('released','lost'))
  ) THEN RAISE EXCEPTION 'invalid runtime allocation transition % -> %',OLD.status,NEW.status; END IF;
  IF NEW.status IN ('released','lost') THEN
    UPDATE agent.runtime_hosts SET
      version=version+1,allocated_vcpu=allocated_vcpu-OLD.vcpu_count,
      allocated_memory_mib=allocated_memory_mib-OLD.memory_mib,
      allocated_disk_mib=allocated_disk_mib-OLD.disk_mib,allocated_sessions=allocated_sessions-1,
      updated_at=NEW.updated_at
    WHERE host_id=OLD.host_id AND allocated_vcpu>=OLD.vcpu_count
      AND allocated_memory_mib>=OLD.memory_mib AND allocated_disk_mib>=OLD.disk_mib AND allocated_sessions>=1;
    GET DIAGNOSTICS changed=ROW_COUNT;
    IF changed<>1 THEN RAISE EXCEPTION 'runtime host capacity release conflict' USING ERRCODE='40001'; END IF;
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER runtime_allocation_lifecycle BEFORE INSERT OR UPDATE OR DELETE ON agent.runtime_allocations
  FOR EACH ROW EXECUTE FUNCTION agent.enforce_runtime_allocation_lifecycle();

CREATE FUNCTION agent.validate_runtime_allocation_session() RETURNS trigger
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
    IF NEW.isolation_kind='firecracker' AND NEW.status<>'requested' AND NOT EXISTS (
      SELECT 1 FROM agent.runtime_allocations a
      WHERE a.tenant_id=NEW.tenant_id AND a.session_id=NEW.id AND a.user_id=NEW.user_id
        AND a.session_version=NEW.version AND a.session_event_id=NEW.last_event_id
        AND ((NEW.status='provisioning' AND a.status='provisioning')
          OR (NEW.status IN ('ready','running','idle') AND a.status='active')
          OR (NEW.status='termination_requested' AND a.status='releasing')
          OR (NEW.status='terminated' AND a.status='released')
          OR (NEW.status='failed' AND a.status='lost'))
    ) THEN RAISE EXCEPTION 'firecracker session lacks matching host allocation'; END IF;
  END IF;
  RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER runtime_allocation_session_guard AFTER INSERT OR UPDATE ON agent.runtime_allocations
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION agent.validate_runtime_allocation_session();
CREATE CONSTRAINT TRIGGER runtime_session_allocation_guard AFTER INSERT OR UPDATE ON agent.runtime_sessions
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION agent.validate_runtime_allocation_session();

CREATE FUNCTION agent.runtime_register_host(
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
    AND agent.runtime_hosts.status IN ('recovering','active','unhealthy')
  RETURNING version INTO result_version;
  IF result_version IS NULL THEN RAISE EXCEPTION 'runtime host registration conflict' USING ERRCODE='40001'; END IF;
  RETURN result_version;
END
$$;

CREATE FUNCTION agent.runtime_heartbeat_host(
  p_host_id text,p_expected_version bigint,p_control_token_hash bytea,p_now timestamptz,p_heartbeat_deadline timestamptz
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,agent SET row_security=off AS $$
DECLARE result_version bigint;
BEGIN
  UPDATE agent.runtime_hosts SET version=version+1,heartbeat_at=p_now,heartbeat_deadline=p_heartbeat_deadline,updated_at=p_now
  WHERE host_id=p_host_id AND version=p_expected_version AND control_token_hash=p_control_token_hash AND status<>'retired'
    AND p_now>=heartbeat_at AND p_heartbeat_deadline>p_now
  RETURNING version INTO result_version;
  IF result_version IS NULL THEN RAISE EXCEPTION 'runtime host heartbeat conflict' USING ERRCODE='40001'; END IF;
  RETURN result_version;
END
$$;

CREATE FUNCTION agent.runtime_set_host_status(
  p_host_id text,p_expected_version bigint,p_control_token_hash bytea,p_status text,p_now timestamptz,p_heartbeat_deadline timestamptz
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,agent SET row_security=off AS $$
DECLARE result_version bigint;
BEGIN
  UPDATE agent.runtime_hosts SET version=version+1,status=p_status,heartbeat_at=p_now,
    heartbeat_deadline=p_heartbeat_deadline,updated_at=p_now
  WHERE host_id=p_host_id AND version=p_expected_version AND control_token_hash=p_control_token_hash
    AND p_now>=heartbeat_at AND p_heartbeat_deadline>p_now
  RETURNING version INTO result_version;
  IF result_version IS NULL THEN RAISE EXCEPTION 'runtime host status conflict' USING ERRCODE='40001'; END IF;
  RETURN result_version;
END
$$;

REVOKE ALL ON FUNCTION agent.runtime_register_host(text,text,text,text,text,text,text,bytea,integer,integer,integer,integer,timestamptz,timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION agent.runtime_heartbeat_host(text,bigint,bytea,timestamptz,timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION agent.runtime_set_host_status(text,bigint,bytea,text,timestamptz,timestamptz) FROM PUBLIC;

ALTER TABLE agent.runtime_allocations ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent.runtime_allocations FORCE ROW LEVEL SECURITY;
CREATE POLICY runtime_allocations_tenant_isolation ON agent.runtime_allocations
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);
