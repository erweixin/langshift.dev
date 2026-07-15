# Realtime database role bootstrap and rotation

This runbook is the production contract for the PostgreSQL identity used by
`realtime-gateway`. Run it independently in every residency cell. The database
operator executes the SQL through an audited break-glass session; the deployer
must not own the database role or see its password.

## Invariants

- The login role is `lites_realtime_service`. It is not a database, schema, table,
  sequence, function, or policy owner.
- It is `NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS` and is not a
  member of another role.
- It can connect to the cell database, use the `agent` schema, and select only
  `agent.events` and `agent.event_cursors`.
- Both tables keep row-level security enabled and forced. Every application
  transaction sets `lites.tenant_id`; an unset or invalid tenant fails closed.
- No default privileges are granted. A future table or function is inaccessible
  until a separately reviewed grant changes this contract.

## Bootstrap

Create the login with a randomly generated password delivered through the
approved secret channel. Never put the password in shell history, a migration,
Terraform state, a ticket, or this repository. Replace `<cell_database>` below
only after recording its exact value in the change approval.

```sql
BEGIN;

ALTER ROLE lites_realtime_service
  NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS;

REVOKE ALL PRIVILEGES ON DATABASE <cell_database> FROM lites_realtime_service;
REVOKE ALL PRIVILEGES ON SCHEMA public, identity, product, agent, contracts
  FROM lites_realtime_service;
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA identity, product, agent, contracts
  FROM lites_realtime_service;
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA identity, product, agent, contracts
  FROM lites_realtime_service;
REVOKE ALL PRIVILEGES ON ALL FUNCTIONS IN SCHEMA identity, product, agent, contracts
  FROM lites_realtime_service;

GRANT CONNECT ON DATABASE <cell_database> TO lites_realtime_service;
GRANT USAGE ON SCHEMA agent TO lites_realtime_service;
GRANT SELECT ON agent.events, agent.event_cursors TO lites_realtime_service;

COMMIT;
```

The role creation statement and initial password are intentionally absent. The
database operator creates the login through the cell's credential broker, then
runs the grant transaction above. If any referenced schema is not present, stop;
do not delete clauses to make the transaction pass.

## Mandatory verification

Run these checks before writing the database URL to Secrets Manager:

```sql
SELECT rolname, rolsuper, rolinherit, rolcreaterole, rolcreatedb, rolbypassrls
FROM pg_roles
WHERE rolname = 'lites_realtime_service';

SELECT granted.rolname AS granted_role
FROM pg_auth_members membership
JOIN pg_roles member ON member.oid = membership.member
JOIN pg_roles granted ON granted.oid = membership.roleid
WHERE member.rolname = 'lites_realtime_service';

SELECT namespace.nspname, class.relname, class.relkind
FROM pg_class class
JOIN pg_namespace namespace ON namespace.oid = class.relnamespace
JOIN pg_roles owner ON owner.oid = class.relowner
WHERE owner.rolname = 'lites_realtime_service';

SELECT namespace.nspname, class.relname, class.relrowsecurity,
       class.relforcerowsecurity
FROM pg_class class
JOIN pg_namespace namespace ON namespace.oid = class.relnamespace
WHERE namespace.nspname = 'agent'
  AND class.relname IN ('events', 'event_cursors');

SELECT table_schema, table_name, privilege_type
FROM information_schema.role_table_grants
WHERE grantee = 'lites_realtime_service'
ORDER BY table_schema, table_name, privilege_type;
```

The membership and ownership queries must return zero rows. The privilege query
must return exactly two `SELECT` rows. Both RLS flags must be true. Then connect
as the service role and prove that reads without `lites.tenant_id` fail, reads
with a test tenant cannot see another tenant, and `INSERT`, `UPDATE`, `DELETE`,
`TRUNCATE`, DDL, function execution, and sequence access are denied. Preserve the
queries, SQLSTATEs, actor, database endpoint, and timestamp as release evidence.

## Secret publication and rotation

1. Encode the TLS-required connection URL in the versioned
   `/<environment>/<cell>/realtime-gateway` secret bundle. Publish it only after
   every mandatory check passes.
2. Wait for External Secrets to materialize the new file. Roll one canary pod,
   verify database and NATS readiness plus tenant-isolation probes, then roll the
   remaining pods while preserving the PodDisruptionBudget.
3. For rotation, change the role password through the credential broker, publish
   a new secret version, and repeat the canary rollout. Existing PostgreSQL
   sessions may remain valid, so terminate old sessions only after all pods use
   the new secret and the connection drain window has elapsed.
4. Revoke the old secret version, confirm there are no sessions from the old
   rollout, and attach secret version IDs, pod image digests, verification output,
   and operator/deployer approvals to the change record.

If a privilege check widens, RLS is not forced, cross-tenant probes return data,
or the canary cannot reconnect with the new secret, stop the rollout and restore
the last known-good secret version. Do not grant broader access as a recovery.
