#!/bin/sh
set -eu

# Docker Desktop engineering fixture only. Production roles are created by the
# deployment control plane with narrower grants; this initializer intentionally
# uses broad table grants so every real handler can run while RLS remains forced
# and is exercised by the macOS product gate.
: "${LITES_SERVICE_PASSWORD:?LITES_SERVICE_PASSWORD is required}"

for migration in /lites-migrations/0*.up.sql; do
  case "$(basename "${migration}")" in
    000001_*) continue ;;
  esac
  psql -v ON_ERROR_STOP=1 --username "${POSTGRES_USER}" --dbname "${POSTGRES_DB}" --file "${migration}"
done

for role in \
  lites_identity_service \
  lites_product_service \
  lites_agent_service \
  lites_scheduler_service \
  lites_behavior_service \
  lites_contract_service \
  lites_realtime_service; do
  psql -v ON_ERROR_STOP=1 --username "${POSTGRES_USER}" --dbname "${POSTGRES_DB}" \
    --set=role_name="${role}" --set=service_password="${LITES_SERVICE_PASSWORD}" <<'SQL'
SELECT format('CREATE ROLE %I LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS', :'role_name', :'service_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname=:'role_name') \gexec
SELECT format('GRANT CONNECT ON DATABASE %I TO %I', current_database(), :'role_name') \gexec
SELECT format('GRANT USAGE ON SCHEMA identity,product,agent,contracts TO %I', :'role_name') \gexec
SELECT format('GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA identity,product,agent,contracts TO %I', :'role_name') \gexec
SELECT format('GRANT USAGE,SELECT,UPDATE ON ALL SEQUENCES IN SCHEMA identity,product,agent,contracts TO %I', :'role_name') \gexec
SELECT format('GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA identity,product,agent,contracts TO %I', :'role_name') \gexec
SQL
done

psql -v ON_ERROR_STOP=1 --username "${POSTGRES_USER}" --dbname "${POSTGRES_DB}" <<'SQL'
-- Behavior snapshots have audited creator/activator foreign keys. The public
-- anonymous tenant therefore receives a dedicated engineering-only projection
-- identity; anonymous browser subjects remain ephemeral and are never promoted
-- into identity.users merely to satisfy those audit fields.
INSERT INTO identity.users(id,normalized_email,email_verified_at,locale,timezone,display_name,status)
VALUES ('20000000-0000-4000-8000-000000000002','engineering-projection@lites.invalid',CURRENT_TIMESTAMP,'en','UTC','Local engineering projection','active')
ON CONFLICT (id) DO NOTHING;

INSERT INTO identity.users(id,normalized_email,email_verified_at,locale,timezone,display_name,status)
VALUES ('20000000-0000-4000-8000-000000000003','engineering-behavior-automation@lites.invalid',CURRENT_TIMESTAMP,'en','UTC','Local behavior automation','active')
ON CONFLICT (id) DO NOTHING;

INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id)
VALUES ('20000000-0000-4000-8000-000000000001','anonymous_system','Lites public onboarding','active','local','20000000-0000-4000-8000-000000000002')
ON CONFLICT (id) DO NOTHING;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM identity.tenants t
    JOIN identity.users u ON u.id=t.owner_user_id
    WHERE t.id='20000000-0000-4000-8000-000000000001'
      AND t.kind='anonymous_system' AND t.status='active'
      AND u.id='20000000-0000-4000-8000-000000000002'
      AND u.normalized_email='engineering-projection@lites.invalid'
      AND u.status='active'
  ) OR NOT EXISTS (
    SELECT 1 FROM identity.users u
    WHERE u.id='20000000-0000-4000-8000-000000000003'
      AND u.normalized_email='engineering-behavior-automation@lites.invalid'
      AND u.status='active'
  ) THEN
    RAISE EXCEPTION 'local behavior identities are inconsistent';
  END IF;
END $$;
SQL

# The Compose healthcheck must not release application dependencies while the
# official Postgres image is still serving its temporary initialization server.
touch /tmp/lites-local-init-complete
