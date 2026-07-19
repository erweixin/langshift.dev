BEGIN;

ALTER TABLE identity.users
  ADD COLUMN IF NOT EXISTS timezone text NOT NULL DEFAULT 'UTC',
  ADD COLUMN IF NOT EXISTS display_name text;

ALTER TABLE identity.users
  DROP CONSTRAINT IF EXISTS users_locale_supported,
  DROP CONSTRAINT IF EXISTS users_timezone_length,
  DROP CONSTRAINT IF EXISTS users_display_name_length,
  ADD CONSTRAINT users_locale_supported CHECK (locale IN ('en','zh-CN')),
  ADD CONSTRAINT users_timezone_length CHECK (char_length(timezone) BETWEEN 1 AND 128),
  ADD CONSTRAINT users_display_name_length CHECK (display_name IS NULL OR char_length(display_name) BETWEEN 1 AND 200);

CREATE OR REPLACE FUNCTION identity.list_user_tenants(
  p_session_id uuid,
  p_user_id uuid,
  p_active_tenant_id uuid,
  p_after_joined_at timestamptz,
  p_after_tenant_id uuid,
  p_limit integer
)
RETURNS TABLE(
  tenant_id uuid,
  tenant_version bigint,
  kind text,
  name text,
  status text,
  region text,
  membership_id uuid,
  membership_version bigint,
  role text,
  joined_at timestamptz,
  updated_at timestamptz
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, identity
SET row_security = off
AS $$
BEGIN
  IF p_session_id IS NULL OR p_user_id IS NULL OR p_active_tenant_id IS NULL
     OR p_limit IS NULL OR p_limit < 1 OR p_limit > 101
     OR (p_after_joined_at IS NULL) <> (p_after_tenant_id IS NULL) THEN
    RAISE EXCEPTION 'invalid tenant list arguments' USING ERRCODE='22023';
  END IF;
  IF NOT EXISTS (
    SELECT 1
    FROM identity.sessions s
    JOIN identity.users u ON u.id=s.user_id
    JOIN identity.tenants active_tenant ON active_tenant.id=s.active_tenant_id
    JOIN identity.memberships active_membership ON active_membership.tenant_id=s.active_tenant_id AND active_membership.user_id=s.user_id
    WHERE s.id=p_session_id AND s.user_id=p_user_id AND s.active_tenant_id=p_active_tenant_id
      AND s.revoked_at IS NULL AND s.expires_at>CURRENT_TIMESTAMP
      AND u.status='active' AND u.email_verified_at IS NOT NULL
      AND active_tenant.status='active' AND active_membership.status='active'
  ) THEN
    RAISE EXCEPTION 'session cannot list tenants' USING ERRCODE='42501';
  END IF;
  RETURN QUERY
    SELECT t.id,t.version,t.kind,t.name,t.status,t.region,m.id,m.version,m.role,m.joined_at,GREATEST(t.updated_at,m.updated_at)
    FROM identity.memberships m
    JOIN identity.tenants t ON t.id=m.tenant_id
    WHERE m.user_id=p_user_id AND m.status='active' AND t.status='active' AND t.kind<>'anonymous_system'
      AND (p_after_joined_at IS NULL OR (m.joined_at,t.id)<(p_after_joined_at,p_after_tenant_id))
    ORDER BY m.joined_at DESC,t.id DESC
    LIMIT p_limit;
END
$$;

REVOKE ALL ON FUNCTION identity.list_user_tenants(uuid,uuid,uuid,timestamptz,uuid,integer) FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_identity_service') THEN
    GRANT EXECUTE ON FUNCTION identity.list_user_tenants(uuid,uuid,uuid,timestamptz,uuid,integer) TO lites_identity_service;
  END IF;
END
$$;

COMMIT;
