CREATE FUNCTION agent.lock_authorized_artifact_export(
  p_tenant_id uuid,
  p_user_id uuid,
  p_run_id uuid,
  p_target_kind text,
  p_target_id uuid,
  p_media_type text
) RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, agent, product
SET row_security = off
AS $$
DECLARE admissible boolean;
BEGIN
  IF p_tenant_id IS NULL OR p_user_id IS NULL OR p_run_id IS NULL OR p_target_id IS NULL
    OR p_target_kind<>'portfolio_export' OR p_media_type NOT IN ('text/html','application/pdf','application/zip') THEN
    RETURN false;
  END IF;
  SELECT true INTO admissible
  FROM product.portfolio_exports p
  JOIN agent.runs r ON r.tenant_id=p.tenant_id AND r.id=p.run_id
  WHERE p.tenant_id=p_tenant_id AND p.user_id=p_user_id AND p.id=p_target_id AND p.run_id=p_run_id
    AND p.status IN ('requested','building') AND r.user_id=p_user_id AND r.status IN ('queued','executing')
    AND ((p.export_format='html' AND p_media_type='text/html')
      OR (p.export_format='pdf' AND p_media_type='application/pdf')
      OR (p.export_format='zip' AND p_media_type='application/zip'))
  FOR SHARE OF p,r;
  RETURN COALESCE(admissible,false);
END
$$;

REVOKE ALL ON FUNCTION agent.lock_authorized_artifact_export(uuid,uuid,uuid,text,uuid,text) FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_agent_service') THEN
    GRANT EXECUTE ON FUNCTION agent.lock_authorized_artifact_export(uuid,uuid,uuid,text,uuid,text) TO lites_agent_service;
  END IF;
END
$$;
