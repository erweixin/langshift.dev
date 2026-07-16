ALTER TABLE product.missions
  ADD CONSTRAINT missions_tenant_id_id_unique UNIQUE (tenant_id,id);

CREATE TABLE agent.conversations (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  mission_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  title text,
  mode text NOT NULL CHECK (mode IN ('coach','task','project')),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','archived')),
  last_run_id uuid,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT conversations_tenant_id_id_unique UNIQUE (tenant_id,id),
  CONSTRAINT conversations_title_contract CHECK (title IS NULL OR (char_length(title) BETWEEN 1 AND 200)),
  CONSTRAINT conversations_tenant_fk FOREIGN KEY (tenant_id)
    REFERENCES identity.tenants(id) ON DELETE CASCADE,
  CONSTRAINT conversations_tenant_mission_fk FOREIGN KEY (tenant_id,mission_id)
    REFERENCES product.missions(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT conversations_tenant_last_run_fk FOREIGN KEY (tenant_id,last_run_id)
    REFERENCES agent.runs(tenant_id,id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);

ALTER TABLE agent.conversations ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent.conversations FORCE ROW LEVEL SECURITY;

CREATE POLICY conversations_tenant_isolation ON agent.conversations
  USING (tenant_id = NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id',true),'')::uuid);

CREATE INDEX conversations_owner_updated_idx
  ON agent.conversations(tenant_id,user_id,updated_at DESC,id);

CREATE INDEX conversations_mission_updated_idx
  ON agent.conversations(tenant_id,mission_id,updated_at DESC,id);

CREATE FUNCTION agent.lock_active_owned_mission(
  p_tenant_id uuid,
  p_user_id uuid,
  p_mission_id uuid
) RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, product
SET row_security = off
AS $$
BEGIN
  IF p_tenant_id IS NULL OR p_user_id IS NULL OR p_mission_id IS NULL THEN
    RAISE EXCEPTION 'mission lock scope is required' USING ERRCODE='22023';
  END IF;
  PERFORM 1
  FROM product.missions
  WHERE tenant_id=p_tenant_id AND user_id=p_user_id AND id=p_mission_id AND status='active'
  FOR KEY SHARE;
  RETURN FOUND;
END
$$;

REVOKE ALL ON FUNCTION agent.lock_active_owned_mission(uuid,uuid,uuid) FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_agent_control_service') THEN
    GRANT SELECT,INSERT,UPDATE ON agent.conversations TO lites_agent_control_service;
    GRANT EXECUTE ON FUNCTION agent.lock_active_owned_mission(uuid,uuid,uuid) TO lites_agent_control_service;
  END IF;
END
$$;
