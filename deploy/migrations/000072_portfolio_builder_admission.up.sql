CREATE FUNCTION agent.lock_owned_portfolio_export(
  p_tenant_id uuid,
  p_user_id uuid,
  p_project_id uuid,
  p_project_version bigint,
  p_workspace_revision text
) RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, agent, product
SET row_security = off
AS $$
DECLARE admissible boolean;
BEGIN
  IF p_tenant_id IS NULL OR p_user_id IS NULL OR p_project_id IS NULL
    OR p_project_version IS NULL OR p_project_version<1 OR NULLIF(p_workspace_revision,'') IS NULL THEN
    RETURN false;
  END IF;
  SELECT true INTO admissible
  FROM product.projects p
  JOIN product.missions m ON m.tenant_id=p.tenant_id AND m.id=p.mission_id
  JOIN product.project_workspace_bindings w ON w.tenant_id=p.tenant_id AND w.project_id=p.id
  WHERE p.tenant_id=p_tenant_id AND p.user_id=p_user_id AND p.id=p_project_id
    AND p.version=p_project_version AND p.status='completed'
    AND NULLIF(p.reflection_ref,'') IS NOT NULL AND NULLIF(p.reflection_hash,'') IS NOT NULL
    AND NULLIF(p.completion_manifest_hash,'') IS NOT NULL
    AND m.user_id=p_user_id AND m.status='active'
    AND w.user_id=p_user_id AND w.head_revision=p_workspace_revision
  FOR SHARE OF p,w;
  RETURN COALESCE(admissible,false);
END
$$;

REVOKE ALL ON FUNCTION agent.lock_owned_portfolio_export(uuid,uuid,uuid,bigint,text) FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    GRANT EXECUTE ON FUNCTION agent.lock_owned_portfolio_export(uuid,uuid,uuid,bigint,text) TO lites_product_service;
  END IF;
END
$$;
