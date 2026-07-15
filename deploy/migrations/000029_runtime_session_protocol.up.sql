CREATE TABLE agent.runtime_policy_snapshots (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  snapshot_key text NOT NULL,
  version bigint NOT NULL,
  policy_hash text NOT NULL,
  trust_tier text NOT NULL,
  isolation_kind text NOT NULL,
  network_mode text NOT NULL,
  network_policy_hash text NOT NULL,
  secret_mode text NOT NULL,
  secret_scope_hash text NOT NULL,
  workspace_mode text NOT NULL,
  image_digest text NOT NULL,
  kernel_digest text,
  rootfs_digest text,
  vcpu_count integer NOT NULL,
  memory_mib integer NOT NULL,
  disk_mib integer NOT NULL,
  pids_max integer NOT NULL,
  maximum_duration_seconds integer NOT NULL,
  idle_timeout_seconds integer NOT NULL,
  kill_grace_seconds integer NOT NULL,
  approval_required boolean NOT NULL,
  policy_manifest jsonb NOT NULL,
  created_event_id uuid NOT NULL,
  created_at timestamptz NOT NULL,
  CONSTRAINT runtime_policy_tenant_id_id_unique UNIQUE (tenant_id,id),
  CONSTRAINT runtime_policy_snapshot_key_unique UNIQUE (tenant_id,snapshot_key),
  CONSTRAINT runtime_policy_session_binding_unique UNIQUE (
    tenant_id,id,snapshot_key,policy_hash,trust_tier,isolation_kind,
    network_policy_hash,secret_scope_hash,workspace_mode
  ),
  CONSTRAINT runtime_policy_scope_contract CHECK (
    version>0 AND snapshot_key ~ '^[a-z][a-z0-9._:-]{2,127}$'
    AND policy_hash ~ '^[0-9a-f]{64}$'
    AND trust_tier IN ('trusted','semi_trusted','untrusted','privileged')
    AND isolation_kind IN ('restricted_container','firecracker')
    AND network_mode IN ('none','broker_only','allowlist_proxy')
    AND network_policy_hash ~ '^sha256:[0-9a-f]{64}$'
    AND secret_mode IN ('none','broker_only','short_lived_injected')
    AND secret_scope_hash ~ '^sha256:[0-9a-f]{64}$'
    AND workspace_mode IN ('none','read_only','read_write')
    AND image_digest ~ '^sha256:[0-9a-f]{64}$'
    AND vcpu_count BETWEEN 1 AND 32 AND memory_mib BETWEEN 128 AND 32768 AND memory_mib%2=0
    AND disk_mib BETWEEN 64 AND 262144 AND pids_max BETWEEN 1 AND 4096
    AND maximum_duration_seconds BETWEEN 1 AND 3600
    AND idle_timeout_seconds BETWEEN 1 AND maximum_duration_seconds
    AND kill_grace_seconds BETWEEN 1 AND 30
    AND jsonb_typeof(policy_manifest)='object'
  ),
  CONSTRAINT runtime_policy_isolation_contract CHECK (
    (trust_tier='trusted' AND isolation_kind='restricted_container' AND kernel_digest IS NULL AND rootfs_digest IS NULL)
    OR (trust_tier IN ('semi_trusted','untrusted','privileged') AND isolation_kind='firecracker'
      AND kernel_digest ~ '^sha256:[0-9a-f]{64}$' AND rootfs_digest ~ '^sha256:[0-9a-f]{64}$')
  ),
  CONSTRAINT runtime_policy_untrusted_contract CHECK (
    trust_tier<>'untrusted' OR (network_mode IN ('none','broker_only') AND secret_mode IN ('none','broker_only'))
  ),
  CONSTRAINT runtime_policy_empty_scope_contract CHECK (
    ((network_mode='none')=(network_policy_hash='sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855'))
    AND ((secret_mode='none')=(secret_scope_hash='sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855'))
    AND (secret_mode<>'short_lived_injected' OR network_mode<>'none')
    AND (trust_tier<>'trusted' OR secret_mode<>'short_lived_injected' OR approval_required)
  ),
  CONSTRAINT runtime_policy_privileged_contract CHECK (trust_tier<>'privileged' OR approval_required),
  CONSTRAINT runtime_policy_event_fk FOREIGN KEY (created_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT runtime_policy_tenant_fk FOREIGN KEY (tenant_id)
    REFERENCES identity.tenants(id) ON DELETE RESTRICT
);

CREATE TABLE agent.runtime_sessions (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  run_id uuid NOT NULL,
  tool_call_id uuid NOT NULL,
  version bigint NOT NULL,
  status text NOT NULL,
  policy_snapshot_id uuid NOT NULL,
  policy_snapshot_key text NOT NULL,
  policy_hash text NOT NULL,
  trust_tier text NOT NULL,
  isolation_kind text NOT NULL,
  workspace_id uuid,
  base_workspace_revision text,
  workspace_mode text NOT NULL,
  network_policy_hash text NOT NULL,
  secret_scope_hash text NOT NULL,
  request_hash text NOT NULL,
  capability_nonce_hash bytea NOT NULL,
  command_id uuid NOT NULL,
  execution_attempt_id uuid NOT NULL,
  execution_fence bigint NOT NULL,
  execution_lease_hash bytea NOT NULL,
  execution_lease_expires_at timestamptz NOT NULL,
  host_id text,
  machine_id text,
  guest_cid bigint,
  provision_attempt_id uuid,
  provision_fence bigint NOT NULL DEFAULT 0,
  provision_lease_hash bytea,
  provision_lease_expires_at timestamptz,
  requested_at timestamptz NOT NULL,
  provisioning_at timestamptz,
  ready_at timestamptz,
  running_at timestamptz,
  last_activity_at timestamptz,
  idle_deadline timestamptz,
  execution_deadline timestamptz NOT NULL,
  termination_requested_at timestamptz,
  kill_deadline timestamptz,
  termination_reason text,
  terminated_at timestamptz,
  failed_at timestamptz,
  failure_code text,
  cleanup_receipt_hash text,
  usage_manifest jsonb,
  created_event_id uuid NOT NULL,
  last_event_id uuid NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  CONSTRAINT runtime_sessions_tenant_id_id_unique UNIQUE (tenant_id,id),
  CONSTRAINT runtime_sessions_execution_unique UNIQUE (tenant_id,command_id,execution_attempt_id),
  CONSTRAINT runtime_sessions_capability_nonce_unique UNIQUE (tenant_id,capability_nonce_hash),
  CONSTRAINT runtime_sessions_scope_contract CHECK (
    version>0 AND status IN ('requested','provisioning','ready','running','idle','termination_requested','terminated','failed')
    AND policy_hash ~ '^[0-9a-f]{64}$'
    AND trust_tier IN ('trusted','semi_trusted','untrusted','privileged')
    AND isolation_kind IN ('restricted_container','firecracker')
    AND workspace_mode IN ('none','read_only','read_write')
    AND ((workspace_mode='none' AND workspace_id IS NULL AND base_workspace_revision IS NULL)
      OR (workspace_mode<>'none' AND workspace_id IS NOT NULL AND NULLIF(base_workspace_revision,'') IS NOT NULL))
    AND network_policy_hash ~ '^sha256:[0-9a-f]{64}$'
    AND secret_scope_hash ~ '^sha256:[0-9a-f]{64}$'
    AND request_hash ~ '^[0-9a-f]{64}$'
    AND octet_length(capability_nonce_hash)=32 AND execution_fence>0
    AND octet_length(execution_lease_hash)=32 AND execution_lease_expires_at>requested_at
    AND execution_deadline>requested_at AND provision_fence>=0
  ),
  CONSTRAINT runtime_sessions_machine_contract CHECK (
    (status='requested' AND host_id IS NULL AND machine_id IS NULL AND guest_cid IS NULL
      AND provision_attempt_id IS NULL AND provision_fence=0 AND provision_lease_hash IS NULL AND provision_lease_expires_at IS NULL)
    OR (status IN ('provisioning','ready','running','idle') AND host_id IS NOT NULL
      AND machine_id ~ '^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$'
      AND guest_cid BETWEEN 3 AND 4294967295 AND provision_attempt_id IS NOT NULL AND provision_fence>0)
    OR (status IN ('termination_requested','terminated','failed') AND (
      (host_id IS NULL AND machine_id IS NULL AND guest_cid IS NULL AND provision_attempt_id IS NULL AND provision_fence=0)
      OR (host_id IS NOT NULL AND machine_id ~ '^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$'
        AND guest_cid BETWEEN 3 AND 4294967295 AND provision_attempt_id IS NOT NULL AND provision_fence>0)
    ))
  ),
  CONSTRAINT runtime_sessions_provision_lease_contract CHECK (
    (status='provisioning' AND octet_length(provision_lease_hash)=32 AND provision_lease_expires_at>provisioning_at)
    OR (status<>'provisioning' AND provision_lease_hash IS NULL AND provision_lease_expires_at IS NULL)
  ),
  CONSTRAINT runtime_sessions_time_contract CHECK (
    (status='requested' AND provisioning_at IS NULL AND ready_at IS NULL AND running_at IS NULL AND last_activity_at IS NULL)
    OR (status='provisioning' AND provisioning_at IS NOT NULL AND ready_at IS NULL AND running_at IS NULL)
    OR (status='ready' AND provisioning_at IS NOT NULL AND ready_at IS NOT NULL AND running_at IS NULL
      AND idle_deadline>ready_at AND idle_deadline<=execution_deadline)
    OR (status='running' AND provisioning_at IS NOT NULL AND ready_at IS NOT NULL AND running_at IS NOT NULL
      AND last_activity_at IS NOT NULL AND idle_deadline IS NULL)
    OR (status='idle' AND provisioning_at IS NOT NULL AND ready_at IS NOT NULL AND running_at IS NOT NULL
      AND last_activity_at IS NOT NULL AND idle_deadline>last_activity_at AND idle_deadline<=execution_deadline)
    OR status IN ('termination_requested','terminated','failed')
  ),
  CONSTRAINT runtime_sessions_termination_contract CHECK (
    (status NOT IN ('termination_requested','terminated','failed') AND termination_requested_at IS NULL
      AND kill_deadline IS NULL AND termination_reason IS NULL AND terminated_at IS NULL AND failed_at IS NULL
      AND failure_code IS NULL AND cleanup_receipt_hash IS NULL AND usage_manifest IS NULL)
    OR (status='termination_requested' AND termination_requested_at IS NOT NULL AND kill_deadline>termination_requested_at
      AND NULLIF(termination_reason,'') IS NOT NULL AND terminated_at IS NULL AND failed_at IS NULL
      AND cleanup_receipt_hash IS NULL AND usage_manifest IS NULL)
    OR (status='terminated' AND termination_requested_at IS NOT NULL AND terminated_at IS NOT NULL
      AND failed_at IS NULL AND failure_code IS NULL AND cleanup_receipt_hash ~ '^[0-9a-f]{64}$'
      AND jsonb_typeof(usage_manifest)='object')
    OR (status='failed' AND failed_at IS NOT NULL AND NULLIF(failure_code,'') IS NOT NULL
      AND cleanup_receipt_hash ~ '^[0-9a-f]{64}$' AND jsonb_typeof(usage_manifest)='object')
  ),
  CONSTRAINT runtime_sessions_policy_fk FOREIGN KEY (
    tenant_id,policy_snapshot_id,policy_snapshot_key,policy_hash,trust_tier,isolation_kind,
    network_policy_hash,secret_scope_hash,workspace_mode
  ) REFERENCES agent.runtime_policy_snapshots(
    tenant_id,id,snapshot_key,policy_hash,trust_tier,isolation_kind,
    network_policy_hash,secret_scope_hash,workspace_mode
  ) ON DELETE RESTRICT,
  CONSTRAINT runtime_sessions_run_fk FOREIGN KEY (tenant_id,run_id)
    REFERENCES agent.runs(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT runtime_sessions_tool_call_fk FOREIGN KEY (tenant_id,tool_call_id)
    REFERENCES agent.tool_calls(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT runtime_sessions_created_event_fk FOREIGN KEY (created_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT runtime_sessions_last_event_fk FOREIGN KEY (last_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);

CREATE UNIQUE INDEX runtime_sessions_machine_unique ON agent.runtime_sessions(host_id,machine_id)
  WHERE machine_id IS NOT NULL;
CREATE INDEX runtime_sessions_recovery_idx ON agent.runtime_sessions(tenant_id,status,provision_lease_expires_at,idle_deadline,execution_deadline,kill_deadline,id)
  WHERE status IN ('provisioning','ready','running','idle','termination_requested');

CREATE TRIGGER runtime_policy_snapshots_append_only BEFORE UPDATE OR DELETE ON agent.runtime_policy_snapshots
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();

CREATE FUNCTION agent.enforce_runtime_session_lifecycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'runtime session deletion is forbidden'; END IF;
  IF OLD.status IN ('terminated','failed') THEN RAISE EXCEPTION 'terminal runtime session mutation is forbidden'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.user_id IS DISTINCT FROM OLD.user_id OR NEW.run_id IS DISTINCT FROM OLD.run_id
    OR NEW.tool_call_id IS DISTINCT FROM OLD.tool_call_id OR NEW.policy_snapshot_id IS DISTINCT FROM OLD.policy_snapshot_id
    OR NEW.policy_snapshot_key IS DISTINCT FROM OLD.policy_snapshot_key OR NEW.policy_hash IS DISTINCT FROM OLD.policy_hash
    OR NEW.trust_tier IS DISTINCT FROM OLD.trust_tier OR NEW.isolation_kind IS DISTINCT FROM OLD.isolation_kind
    OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id OR NEW.base_workspace_revision IS DISTINCT FROM OLD.base_workspace_revision
    OR NEW.workspace_mode IS DISTINCT FROM OLD.workspace_mode OR NEW.network_policy_hash IS DISTINCT FROM OLD.network_policy_hash
    OR NEW.secret_scope_hash IS DISTINCT FROM OLD.secret_scope_hash OR NEW.request_hash IS DISTINCT FROM OLD.request_hash
    OR NEW.capability_nonce_hash IS DISTINCT FROM OLD.capability_nonce_hash OR NEW.command_id IS DISTINCT FROM OLD.command_id
    OR NEW.execution_attempt_id IS DISTINCT FROM OLD.execution_attempt_id OR NEW.execution_fence IS DISTINCT FROM OLD.execution_fence
    OR NEW.execution_lease_hash IS DISTINCT FROM OLD.execution_lease_hash OR NEW.execution_lease_expires_at IS DISTINCT FROM OLD.execution_lease_expires_at
    OR NEW.execution_deadline IS DISTINCT FROM OLD.execution_deadline OR NEW.created_event_id IS DISTINCT FROM OLD.created_event_id
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'immutable runtime session scope mutation is forbidden';
  END IF;
  IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at OR NEW.last_event_id=OLD.last_event_id THEN
    RAISE EXCEPTION 'runtime session version and event must advance exactly once';
  END IF;
  IF OLD.status='requested' AND NEW.status IN ('provisioning','termination_requested') THEN
    IF NEW.status='provisioning' AND NEW.provision_fence<>1 THEN RAISE EXCEPTION 'initial runtime provision fence must be one'; END IF;
    RETURN NEW;
  END IF;
  IF OLD.status='provisioning' AND NEW.status IN ('ready','failed','termination_requested') THEN RETURN NEW; END IF;
  IF OLD.status='ready' AND NEW.status IN ('running','termination_requested') THEN RETURN NEW; END IF;
  IF OLD.status='running' AND NEW.status IN ('idle','termination_requested') THEN RETURN NEW; END IF;
  IF OLD.status='idle' AND NEW.status IN ('running','termination_requested') THEN RETURN NEW; END IF;
  IF OLD.status='termination_requested' AND NEW.status IN ('terminated','failed') THEN RETURN NEW; END IF;
  RAISE EXCEPTION 'invalid runtime session transition % -> %',OLD.status,NEW.status;
END
$$;

CREATE TRIGGER runtime_session_lifecycle BEFORE UPDATE OR DELETE ON agent.runtime_sessions
  FOR EACH ROW EXECUTE FUNCTION agent.enforce_runtime_session_lifecycle();

CREATE FUNCTION agent.validate_runtime_policy_event() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM agent.events e WHERE e.id=NEW.created_event_id AND e.tenant_id=NEW.tenant_id
      AND e.aggregate_kind='runtime_policy' AND e.aggregate_id=NEW.id AND e.aggregate_version=NEW.version
      AND e.event_type='RuntimePolicySnapshotCreated'
  ) THEN RAISE EXCEPTION 'runtime policy snapshot lacks matching event fact'; END IF;
  RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER runtime_policy_event_guard AFTER INSERT ON agent.runtime_policy_snapshots
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION agent.validate_runtime_policy_event();

CREATE FUNCTION agent.validate_runtime_session_event() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE expected_type text;
BEGIN
  expected_type := CASE NEW.status
    WHEN 'requested' THEN 'RuntimeSessionRequested'
    WHEN 'provisioning' THEN 'RuntimeSessionProvisioningStarted'
    WHEN 'ready' THEN 'RuntimeSessionReady'
    WHEN 'running' THEN 'RuntimeSessionExecutionStarted'
    WHEN 'idle' THEN 'RuntimeSessionIdle'
    WHEN 'termination_requested' THEN 'RuntimeTerminationRequested'
    WHEN 'terminated' THEN 'RuntimeSessionTerminated'
    WHEN 'failed' THEN 'RuntimeSessionFailed'
  END;
  IF NOT EXISTS (
    SELECT 1 FROM agent.events e WHERE e.id=NEW.last_event_id AND e.tenant_id=NEW.tenant_id
      AND e.user_id=NEW.user_id AND e.aggregate_kind='runtime_session' AND e.aggregate_id=NEW.id
      AND e.aggregate_version=NEW.version AND e.event_type=expected_type
  ) OR (NEW.version=1 AND NEW.created_event_id<>NEW.last_event_id) THEN
    RAISE EXCEPTION 'runtime session transition lacks matching event fact';
  END IF;
  IF NEW.version=1 AND NOT EXISTS (
    SELECT 1 FROM agent.tool_calls t
    WHERE t.tenant_id=NEW.tenant_id AND t.id=NEW.tool_call_id AND t.user_id=NEW.user_id AND t.run_id=NEW.run_id
      AND t.status IN ('executing','preparing_approval') AND t.execution_mode='worker_runtime'
      AND t.active_command_id=NEW.command_id AND t.active_attempt_id=NEW.execution_attempt_id
      AND t.current_fence=NEW.execution_fence AND t.lease_token_hash=NEW.execution_lease_hash
      AND t.lease_expires_at=NEW.execution_lease_expires_at AND t.lease_expires_at>NEW.requested_at
  ) THEN RAISE EXCEPTION 'runtime session lacks exact fenced tool execution right'; END IF;
  RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER runtime_session_event_guard AFTER INSERT OR UPDATE ON agent.runtime_sessions
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION agent.validate_runtime_session_event();

CREATE FUNCTION agent.list_runtime_recovery_tenants(
  p_store_epoch uuid,p_after uuid,p_limit integer,p_shard_index integer,p_shard_count integer,p_now timestamptz
) RETURNS TABLE(tenant_id text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,agent SET row_security=off AS $$
BEGIN
  IF p_store_epoch IS NULL OR p_limit IS NULL OR p_limit<1 OR p_limit>5000 OR p_shard_count IS NULL OR p_shard_count<1
    OR p_shard_index IS NULL OR p_shard_index<0 OR p_shard_index>=p_shard_count OR p_now IS NULL
  THEN RAISE EXCEPTION 'invalid runtime recovery tenant scan arguments' USING ERRCODE='22023'; END IF;
  RETURN QUERY SELECT s.tenant_id::text FROM agent.runtime_sessions s
    JOIN agent.events e ON e.tenant_id=s.tenant_id AND e.id=s.created_event_id AND e.store_epoch=p_store_epoch
    WHERE ((s.status='provisioning' AND s.provision_lease_expires_at<=p_now)
      OR (s.status IN ('ready','idle') AND s.idle_deadline<=p_now)
      OR (s.status='running' AND s.execution_deadline<=p_now)
      OR (s.status='termination_requested' AND s.kill_deadline<=p_now))
      AND (p_after IS NULL OR s.tenant_id>p_after)
      AND ((hashtextextended(s.tenant_id::text,0) & 9223372036854775807) % p_shard_count)=p_shard_index
    GROUP BY s.tenant_id ORDER BY s.tenant_id LIMIT p_limit;
END
$$;

REVOKE ALL ON FUNCTION agent.list_runtime_recovery_tenants(uuid,uuid,integer,integer,integer,timestamptz) FROM PUBLIC;

ALTER TABLE agent.runtime_policy_snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent.runtime_policy_snapshots FORCE ROW LEVEL SECURITY;
CREATE POLICY runtime_policy_snapshots_tenant_isolation ON agent.runtime_policy_snapshots
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);

ALTER TABLE agent.runtime_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent.runtime_sessions FORCE ROW LEVEL SECURITY;
CREATE POLICY runtime_sessions_tenant_isolation ON agent.runtime_sessions
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);
