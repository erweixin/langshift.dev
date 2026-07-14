-- Generated contract DDL. Changes must originate in generate-phase1-assets.mjs.

BEGIN;

CREATE SCHEMA IF NOT EXISTS "identity";

CREATE SCHEMA IF NOT EXISTS "product";

CREATE SCHEMA IF NOT EXISTS "agent";

CREATE SCHEMA IF NOT EXISTS "contracts";

REVOKE ALL ON SCHEMA "identity" FROM PUBLIC;

CREATE OR REPLACE FUNCTION "agent".reject_append_only_mutation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'append-only table % cannot be mutated', TG_TABLE_NAME USING ERRCODE = '55000'; END $$;

CREATE TABLE IF NOT EXISTS "identity"."users" (
  id uuid PRIMARY KEY,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  normalized_email text NOT NULL,
  email_verified_at timestamptz,
  locale text NOT NULL DEFAULT 'en',
  status text NOT NULL,
  deletion_requested_at timestamptz,
  UNIQUE ("normalized_email")
);

CREATE TABLE IF NOT EXISTS "identity"."password_credentials" (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL UNIQUE,
  password_hash bytea NOT NULL,
  algorithm text NOT NULL CHECK (algorithm = 'argon2id'),
  parameters jsonb NOT NULL,
  password_changed_at timestamptz NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS "identity"."email_verifications" (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL,
  token_hash bytea NOT NULL,
  email text NOT NULL,
  expires_at timestamptz NOT NULL,
  used_at timestamptz,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE ("token_hash")
);

CREATE TABLE IF NOT EXISTS "identity"."password_reset_requests" (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL,
  token_hash bytea NOT NULL,
  expires_at timestamptz NOT NULL,
  used_at timestamptz,
  revoked_at timestamptz,
  request_ip_hash bytea NOT NULL,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE ("token_hash")
);

CREATE TABLE IF NOT EXISTS "identity"."sessions" (
  id uuid PRIMARY KEY,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  user_id uuid NOT NULL,
  active_tenant_id uuid NOT NULL,
  token_hash bytea NOT NULL,
  csrf_secret_hash bytea NOT NULL,
  device_label text,
  ip_hash bytea NOT NULL,
  user_agent_hash bytea NOT NULL,
  last_seen_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  revoked_at timestamptz,
  reauthenticated_at timestamptz,
  UNIQUE ("token_hash")
);

CREATE TABLE IF NOT EXISTS "identity"."tenants" (
  id uuid PRIMARY KEY,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  kind text NOT NULL CHECK (kind IN ('personal','enterprise','anonymous_system')),
  name text NOT NULL,
  status text NOT NULL CHECK (status IN ('active','suspended','terminated')),
  region text NOT NULL,
  owner_user_id uuid
);

CREATE TABLE IF NOT EXISTS "identity"."memberships" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  user_id uuid NOT NULL,
  role text NOT NULL CHECK (role IN ('owner','admin','contract_admin','program_manager','reviewer','member')),
  status text NOT NULL CHECK (status IN ('active','suspended','left')),
  joined_at timestamptz NOT NULL,
  deactivated_at timestamptz,
  UNIQUE ("tenant_id", "user_id")
);

ALTER TABLE "identity"."memberships" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "identity"."memberships" FORCE ROW LEVEL SECURITY;

CREATE POLICY "memberships_tenant_isolation" ON "identity"."memberships" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "identity"."invitations" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  normalized_email text NOT NULL,
  role text NOT NULL,
  token_hash bytea NOT NULL,
  invited_by uuid NOT NULL,
  expires_at timestamptz NOT NULL,
  accepted_at timestamptz,
  rejected_at timestamptz,
  revoked_at timestamptz,
  batch_key text,
  UNIQUE ("token_hash"),
  UNIQUE ("tenant_id", "normalized_email", "batch_key")
);

ALTER TABLE "identity"."invitations" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "identity"."invitations" FORCE ROW LEVEL SECURITY;

CREATE POLICY "invitations_tenant_isolation" ON "identity"."invitations" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "identity"."security_events" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  subject_user_id uuid,
  actor_user_id uuid,
  event_type text NOT NULL,
  request_id text NOT NULL,
  ip_hash bytea,
  user_agent_hash bytea,
  details jsonb NOT NULL,
  occurred_at timestamptz NOT NULL
);

ALTER TABLE "identity"."security_events" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "identity"."security_events" FORCE ROW LEVEL SECURITY;

CREATE POLICY "security_events_tenant_isolation" ON "identity"."security_events" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TRIGGER "security_events_append_only" BEFORE UPDATE OR DELETE ON "identity"."security_events" FOR EACH ROW EXECUTE FUNCTION "agent".reject_append_only_mutation();

CREATE TABLE IF NOT EXISTS "identity"."consent_records" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  purpose text NOT NULL,
  policy_version text NOT NULL,
  granted_at timestamptz NOT NULL,
  withdrawn_at timestamptz,
  evidence_hash text NOT NULL,
  UNIQUE ("tenant_id", "user_id", "purpose", "version")
);

ALTER TABLE "identity"."consent_records" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "identity"."consent_records" FORCE ROW LEVEL SECURITY;

CREATE POLICY "consent_records_tenant_isolation" ON "identity"."consent_records" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "identity"."account_erasure_requests" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  status text NOT NULL CHECK (status IN ('requested','processing','completed','cancelled','failed')),
  requested_at timestamptz NOT NULL,
  scheduled_for timestamptz NOT NULL,
  cancelled_at timestamptz,
  completed_at timestamptz,
  receipt_manifest_hash text
);

ALTER TABLE "identity"."account_erasure_requests" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "identity"."account_erasure_requests" FORCE ROW LEVEL SECURITY;

CREATE POLICY "account_erasure_requests_tenant_isolation" ON "identity"."account_erasure_requests" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "identity"."anonymous_subjects" (
  id uuid PRIMARY KEY,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  anonymous_subject_hash bytea NOT NULL,
  ephemeral_user_id uuid NOT NULL,
  system_tenant_id uuid NOT NULL,
  expires_at timestamptz NOT NULL,
  reserved_at timestamptz,
  deleted_at timestamptz,
  UNIQUE ("anonymous_subject_hash"),
  UNIQUE ("ephemeral_user_id")
);

CREATE TABLE IF NOT EXISTS "identity"."onboarding_sessions" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  anonymous_subject_id uuid,
  status text NOT NULL,
  locale text NOT NULL,
  current_role_input jsonb NOT NULL,
  target_role_input jsonb NOT NULL,
  experience_payload_ref text,
  confirmed_claim_ids jsonb NOT NULL,
  route_revision_id uuid,
  expires_at timestamptz NOT NULL
);

ALTER TABLE "identity"."onboarding_sessions" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "identity"."onboarding_sessions" FORCE ROW LEVEL SECURITY;

CREATE POLICY "onboarding_sessions_tenant_isolation" ON "identity"."onboarding_sessions" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "identity"."onboarding_claims" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  anonymous_subject_id uuid NOT NULL,
  onboarding_session_id uuid NOT NULL,
  claim_key text NOT NULL,
  status text NOT NULL CHECK (status IN ('available','reserved','destination_committed','erasing','claimed','expired','manual_review')),
  source_route_revision_id uuid NOT NULL,
  target_tenant_id uuid,
  target_user_id uuid,
  target_mission_id uuid,
  destination_commit_event_id uuid,
  reserved_at timestamptz,
  destination_committed_at timestamptz,
  erasing_at timestamptz,
  claimed_at timestamptz,
  expires_at timestamptz NOT NULL,
  UNIQUE ("claim_key")
);

ALTER TABLE "identity"."onboarding_claims" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "identity"."onboarding_claims" FORCE ROW LEVEL SECURITY;

CREATE POLICY "onboarding_claims_tenant_isolation" ON "identity"."onboarding_claims" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "identity"."anonymous_erasure_receipts" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  claim_id uuid NOT NULL,
  surface text NOT NULL,
  receipt_hash text NOT NULL,
  erased_at timestamptz NOT NULL,
  details jsonb NOT NULL,
  UNIQUE ("claim_id", "surface")
);

ALTER TABLE "identity"."anonymous_erasure_receipts" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "identity"."anonymous_erasure_receipts" FORCE ROW LEVEL SECURITY;

CREATE POLICY "anonymous_erasure_receipts_tenant_isolation" ON "identity"."anonymous_erasure_receipts" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TRIGGER "anonymous_erasure_receipts_append_only" BEFORE UPDATE OR DELETE ON "identity"."anonymous_erasure_receipts" FOR EACH ROW EXECUTE FUNCTION "agent".reject_append_only_mutation();

CREATE TABLE IF NOT EXISTS "product"."role_profiles" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  slug text NOT NULL,
  revision integer NOT NULL CHECK (revision > 0),
  status text NOT NULL,
  spec jsonb NOT NULL,
  locale text NOT NULL,
  source_manifest jsonb NOT NULL,
  UNIQUE ("tenant_id", "slug", "revision")
);

ALTER TABLE "product"."role_profiles" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."role_profiles" FORCE ROW LEVEL SECURITY;

CREATE POLICY "role_profiles_tenant_isolation" ON "product"."role_profiles" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."capabilities" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  slug text NOT NULL,
  revision integer NOT NULL CHECK (revision > 0),
  status text NOT NULL,
  spec jsonb NOT NULL,
  parent_capability_id uuid,
  evidence_guidance jsonb NOT NULL,
  UNIQUE ("tenant_id", "slug", "revision")
);

ALTER TABLE "product"."capabilities" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."capabilities" FORCE ROW LEVEL SECURITY;

CREATE POLICY "capabilities_tenant_isolation" ON "product"."capabilities" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."role_capability_requirements" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  role_profile_id uuid NOT NULL,
  capability_id uuid NOT NULL,
  requirement_level text NOT NULL,
  rationale text NOT NULL,
  revision integer NOT NULL,
  UNIQUE ("tenant_id", "role_profile_id", "capability_id", "revision")
);

ALTER TABLE "product"."role_capability_requirements" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."role_capability_requirements" FORCE ROW LEVEL SECURITY;

CREATE POLICY "role_capability_requirements_tenant_isolation" ON "product"."role_capability_requirements" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."transition_templates" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  slug text NOT NULL,
  revision integer NOT NULL CHECK (revision > 0),
  status text NOT NULL,
  spec jsonb NOT NULL,
  source_role_profile_id uuid NOT NULL,
  target_role_profile_id uuid NOT NULL,
  bridge_spec jsonb NOT NULL,
  UNIQUE ("tenant_id", "slug", "revision")
);

ALTER TABLE "product"."transition_templates" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."transition_templates" FORCE ROW LEVEL SECURITY;

CREATE POLICY "transition_templates_tenant_isolation" ON "product"."transition_templates" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."content_revisions" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  slug text NOT NULL,
  revision integer NOT NULL CHECK (revision > 0),
  status text NOT NULL,
  spec jsonb NOT NULL,
  locale text NOT NULL,
  content_type text NOT NULL,
  body_ref text NOT NULL,
  source_manifest jsonb NOT NULL,
  UNIQUE ("tenant_id", "slug", "revision")
);

ALTER TABLE "product"."content_revisions" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."content_revisions" FORCE ROW LEVEL SECURITY;

CREATE POLICY "content_revisions_tenant_isolation" ON "product"."content_revisions" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."task_templates" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  slug text NOT NULL,
  revision integer NOT NULL CHECK (revision > 0),
  status text NOT NULL,
  spec jsonb NOT NULL,
  practice_kind text NOT NULL CHECK (practice_kind IN ('code','writing','design')),
  estimated_minutes integer NOT NULL,
  instructions_ref text NOT NULL,
  validation_spec jsonb NOT NULL,
  UNIQUE ("tenant_id", "slug", "revision")
);

ALTER TABLE "product"."task_templates" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."task_templates" FORCE ROW LEVEL SECURITY;

CREATE POLICY "task_templates_tenant_isolation" ON "product"."task_templates" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."rubric_versions" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  slug text NOT NULL,
  revision integer NOT NULL CHECK (revision > 0),
  status text NOT NULL,
  spec jsonb NOT NULL,
  dimensions jsonb NOT NULL,
  scoring_rules jsonb NOT NULL,
  UNIQUE ("tenant_id", "slug", "revision")
);

ALTER TABLE "product"."rubric_versions" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."rubric_versions" FORCE ROW LEVEL SECURITY;

CREATE POLICY "rubric_versions_tenant_isolation" ON "product"."rubric_versions" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."missions" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  status text NOT NULL CHECK (status IN ('draft','active','paused','completed','archived')),
  source_role_profile_id uuid,
  target_role_profile_id uuid NOT NULL,
  goal_payload_ref text,
  route_version bigint NOT NULL DEFAULT 0,
  claim_set_hash text NOT NULL,
  current_route_revision_id uuid,
  completed_at timestamptz,
  archived_at timestamptz
);

ALTER TABLE "product"."missions" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."missions" FORCE ROW LEVEL SECURITY;

CREATE POLICY "missions_tenant_isolation" ON "product"."missions" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."mission_imports" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  claim_key text NOT NULL,
  mission_id uuid NOT NULL,
  source_route_revision_id uuid NOT NULL,
  commit_event_id uuid NOT NULL,
  imported_at timestamptz NOT NULL,
  UNIQUE ("claim_key")
);

ALTER TABLE "product"."mission_imports" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."mission_imports" FORCE ROW LEVEL SECURITY;

CREATE POLICY "mission_imports_tenant_isolation" ON "product"."mission_imports" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."mission_focuses" (
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  mission_id uuid,
  focus_version bigint NOT NULL DEFAULT 1,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (tenant_id,user_id)
);

ALTER TABLE "product"."mission_focuses" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."mission_focuses" FORCE ROW LEVEL SECURITY;

CREATE POLICY "mission_focuses_tenant_isolation" ON "product"."mission_focuses" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."route_revisions" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  mission_id uuid NOT NULL,
  route_version bigint NOT NULL,
  status text NOT NULL CHECK (status IN ('generating','proposed','accepted','stale','superseded','failed')),
  claim_set_hash text NOT NULL,
  base_route_version bigint NOT NULL,
  input_manifest jsonb NOT NULL,
  route_payload_ref text,
  agent_profile_snapshot_id text NOT NULL,
  ontology_snapshot_id text NOT NULL,
  content_snapshot_id text NOT NULL,
  accepted_at timestamptz,
  stale_reason text,
  UNIQUE ("tenant_id", "mission_id", "route_version")
);

ALTER TABLE "product"."route_revisions" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."route_revisions" FORCE ROW LEVEL SECURITY;

CREATE POLICY "route_revisions_tenant_isolation" ON "product"."route_revisions" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."capability_claims" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  claim_identity_id uuid NOT NULL,
  claim_revision integer NOT NULL,
  mission_id uuid NOT NULL,
  capability_id uuid NOT NULL,
  status text NOT NULL CHECK (status IN ('active','disputed','superseded','withdrawn','rejected')),
  origin text NOT NULL CHECK (origin IN ('inferred','user_asserted','system_derived','reviewer_asserted')),
  verification_level text NOT NULL CHECK (verification_level IN ('inferred','user_confirmed','demonstrated','applied','reviewer_verified')),
  statement_ref text NOT NULL,
  supersedes_claim_id uuid,
  reason_ref text,
  recorded_at timestamptz NOT NULL,
  UNIQUE ("tenant_id", "claim_identity_id", "claim_revision")
);

ALTER TABLE "product"."capability_claims" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."capability_claims" FORCE ROW LEVEL SECURITY;

CREATE POLICY "capability_claims_tenant_isolation" ON "product"."capability_claims" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TRIGGER "capability_claims_append_only" BEFORE UPDATE OR DELETE ON "product"."capability_claims" FOR EACH ROW EXECUTE FUNCTION "agent".reject_append_only_mutation();

CREATE TABLE IF NOT EXISTS "product"."evidence" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  mission_id uuid NOT NULL,
  evidence_type text NOT NULL,
  status text NOT NULL CHECK (status IN ('recorded','verified','disputed','invalidated')),
  source_kind text NOT NULL,
  source_id uuid,
  payload_ref text NOT NULL,
  content_hash text NOT NULL,
  recorded_at timestamptz NOT NULL,
  invalidated_at timestamptz
);

ALTER TABLE "product"."evidence" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."evidence" FORCE ROW LEVEL SECURITY;

CREATE POLICY "evidence_tenant_isolation" ON "product"."evidence" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TRIGGER "evidence_append_only" BEFORE UPDATE OR DELETE ON "product"."evidence" FOR EACH ROW EXECUTE FUNCTION "agent".reject_append_only_mutation();

CREATE TABLE IF NOT EXISTS "product"."claim_evidence_links" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  claim_id uuid NOT NULL,
  evidence_id uuid NOT NULL,
  relation text NOT NULL CHECK (relation IN ('supports','contradicts')),
  recorded_at timestamptz NOT NULL,
  UNIQUE ("tenant_id", "claim_id", "evidence_id", "relation")
);

ALTER TABLE "product"."claim_evidence_links" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."claim_evidence_links" FORCE ROW LEVEL SECURITY;

CREATE POLICY "claim_evidence_links_tenant_isolation" ON "product"."claim_evidence_links" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TRIGGER "claim_evidence_links_append_only" BEFORE UPDATE OR DELETE ON "product"."claim_evidence_links" FOR EACH ROW EXECUTE FUNCTION "agent".reject_append_only_mutation();

CREATE TABLE IF NOT EXISTS "product"."daily_tasks" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  mission_id uuid NOT NULL,
  route_revision_id uuid NOT NULL,
  status text NOT NULL CHECK (status IN ('scheduled','in_progress','submitted','reviewing','completed','skipped','rescheduled')),
  practice_kind text NOT NULL,
  task_payload_ref text NOT NULL,
  causal_manifest jsonb NOT NULL,
  estimated_minutes integer NOT NULL,
  scheduled_for date NOT NULL,
  completed_at timestamptz
);

ALTER TABLE "product"."daily_tasks" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."daily_tasks" FORCE ROW LEVEL SECURITY;

CREATE POLICY "daily_tasks_tenant_isolation" ON "product"."daily_tasks" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."submissions" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  daily_task_id uuid NOT NULL,
  submission_revision integer NOT NULL,
  payload_ref text NOT NULL,
  content_hash text NOT NULL,
  understanding_ref text,
  submitted_at timestamptz NOT NULL,
  UNIQUE ("tenant_id", "daily_task_id", "submission_revision")
);

ALTER TABLE "product"."submissions" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."submissions" FORCE ROW LEVEL SECURITY;

CREATE POLICY "submissions_tenant_isolation" ON "product"."submissions" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TRIGGER "submissions_append_only" BEFORE UPDATE OR DELETE ON "product"."submissions" FOR EACH ROW EXECUTE FUNCTION "agent".reject_append_only_mutation();

CREATE TABLE IF NOT EXISTS "product"."reviews" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  submission_id uuid NOT NULL,
  rubric_version_id uuid NOT NULL,
  status text NOT NULL,
  deterministic_results jsonb NOT NULL,
  review_payload_ref text NOT NULL,
  evidence_id uuid,
  reviewed_at timestamptz NOT NULL
);

ALTER TABLE "product"."reviews" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."reviews" FORCE ROW LEVEL SECURITY;

CREATE POLICY "reviews_tenant_isolation" ON "product"."reviews" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TRIGGER "reviews_append_only" BEFORE UPDATE OR DELETE ON "product"."reviews" FOR EACH ROW EXECUTE FUNCTION "agent".reject_append_only_mutation();

CREATE TABLE IF NOT EXISTS "product"."projects" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  mission_id uuid NOT NULL,
  accepted_route_revision_id uuid NOT NULL,
  status text NOT NULL CHECK (status IN ('draft','active','blocked','completed','archived')),
  project_kind text NOT NULL CHECK (project_kind IN ('code','writing','design')),
  title text NOT NULL,
  brief_ref text NOT NULL,
  reflection_ref text,
  completed_at timestamptz
);

ALTER TABLE "product"."projects" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."projects" FORCE ROW LEVEL SECURITY;

CREATE POLICY "projects_tenant_isolation" ON "product"."projects" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."project_milestones" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  project_id uuid NOT NULL,
  sequence integer NOT NULL,
  required boolean NOT NULL DEFAULT true,
  status text NOT NULL CHECK (status IN ('planned','in_progress','submitted','verified','rework','completed')),
  title text NOT NULL,
  acceptance_spec jsonb NOT NULL,
  completed_at timestamptz,
  UNIQUE ("tenant_id", "project_id", "sequence")
);

ALTER TABLE "product"."project_milestones" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."project_milestones" FORCE ROW LEVEL SECURITY;

CREATE POLICY "project_milestones_tenant_isolation" ON "product"."project_milestones" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."project_workspace_bindings" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  project_id uuid NOT NULL UNIQUE,
  workspace_id uuid NOT NULL,
  branch_name text NOT NULL,
  base_revision text NOT NULL,
  head_revision text NOT NULL,
  binding_manifest_hash text NOT NULL
);

ALTER TABLE "product"."project_workspace_bindings" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."project_workspace_bindings" FORCE ROW LEVEL SECURITY;

CREATE POLICY "project_workspace_bindings_tenant_isolation" ON "product"."project_workspace_bindings" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."project_test_runs" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  project_id uuid NOT NULL,
  milestone_id uuid,
  workspace_revision text NOT NULL,
  validation_kind text NOT NULL,
  result text NOT NULL,
  result_manifest jsonb NOT NULL,
  recorded_at timestamptz NOT NULL
);

ALTER TABLE "product"."project_test_runs" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."project_test_runs" FORCE ROW LEVEL SECURITY;

CREATE POLICY "project_test_runs_tenant_isolation" ON "product"."project_test_runs" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TRIGGER "project_test_runs_append_only" BEFORE UPDATE OR DELETE ON "product"."project_test_runs" FOR EACH ROW EXECUTE FUNCTION "agent".reject_append_only_mutation();

CREATE TABLE IF NOT EXISTS "product"."artifacts" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  project_id uuid NOT NULL,
  artifact_kind text NOT NULL,
  status text NOT NULL,
  current_revision integer NOT NULL DEFAULT 0
);

ALTER TABLE "product"."artifacts" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."artifacts" FORCE ROW LEVEL SECURITY;

CREATE POLICY "artifacts_tenant_isolation" ON "product"."artifacts" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."artifact_revisions" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  artifact_id uuid NOT NULL,
  revision integer NOT NULL,
  content_hash text NOT NULL,
  object_ref text NOT NULL,
  scan_status text NOT NULL,
  evidence_manifest jsonb NOT NULL,
  UNIQUE ("tenant_id", "artifact_id", "revision")
);

ALTER TABLE "product"."artifact_revisions" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."artifact_revisions" FORCE ROW LEVEL SECURITY;

CREATE POLICY "artifact_revisions_tenant_isolation" ON "product"."artifact_revisions" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TRIGGER "artifact_revisions_append_only" BEFORE UPDATE OR DELETE ON "product"."artifact_revisions" FOR EACH ROW EXECUTE FUNCTION "agent".reject_append_only_mutation();

CREATE TABLE IF NOT EXISTS "product"."portfolio_exports" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  project_id uuid NOT NULL,
  status text NOT NULL,
  revision_manifest jsonb NOT NULL,
  object_ref text,
  content_hash text,
  expires_at timestamptz,
  completed_at timestamptz
);

ALTER TABLE "product"."portfolio_exports" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."portfolio_exports" FORCE ROW LEVEL SECURITY;

CREATE POLICY "portfolio_exports_tenant_isolation" ON "product"."portfolio_exports" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."share_grants" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  grantee_user_id uuid NOT NULL,
  resource_kind text NOT NULL,
  resource_id uuid NOT NULL,
  resource_revision text NOT NULL,
  scope jsonb NOT NULL,
  expires_at timestamptz,
  revoked_at timestamptz
);

ALTER TABLE "product"."share_grants" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."share_grants" FORCE ROW LEVEL SECURITY;

CREATE POLICY "share_grants_tenant_isolation" ON "product"."share_grants" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."programs" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  name text NOT NULL,
  status text NOT NULL,
  owner_user_id uuid NOT NULL,
  settings jsonb NOT NULL
);

ALTER TABLE "product"."programs" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."programs" FORCE ROW LEVEL SECURITY;

CREATE POLICY "programs_tenant_isolation" ON "product"."programs" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."cohorts" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  program_id uuid NOT NULL,
  name text NOT NULL,
  starts_at timestamptz,
  ends_at timestamptz,
  status text NOT NULL
);

ALTER TABLE "product"."cohorts" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."cohorts" FORCE ROW LEVEL SECURITY;

CREATE POLICY "cohorts_tenant_isolation" ON "product"."cohorts" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."enrollments" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  cohort_id uuid NOT NULL,
  user_id uuid NOT NULL,
  status text NOT NULL,
  enrolled_at timestamptz NOT NULL,
  left_at timestamptz,
  UNIQUE ("tenant_id", "cohort_id", "user_id")
);

ALTER TABLE "product"."enrollments" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."enrollments" FORCE ROW LEVEL SECURITY;

CREATE POLICY "enrollments_tenant_isolation" ON "product"."enrollments" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."role_packs" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  program_id uuid NOT NULL,
  revision integer NOT NULL,
  status text NOT NULL,
  role_profile_ids jsonb NOT NULL,
  task_template_ids jsonb NOT NULL,
  published_at timestamptz,
  UNIQUE ("tenant_id", "program_id", "revision")
);

ALTER TABLE "product"."role_packs" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."role_packs" FORCE ROW LEVEL SECURITY;

CREATE POLICY "role_packs_tenant_isolation" ON "product"."role_packs" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."aggregate_metrics" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  metric_key text NOT NULL,
  allowed_dimensions jsonb NOT NULL,
  allowed_time_buckets jsonb NOT NULL,
  minimum_cell_size integer NOT NULL DEFAULT 5,
  minimum_complement_size integer NOT NULL DEFAULT 5,
  UNIQUE ("tenant_id", "metric_key")
);

ALTER TABLE "product"."aggregate_metrics" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."aggregate_metrics" FORCE ROW LEVEL SECURITY;

CREATE POLICY "aggregate_metrics_tenant_isolation" ON "product"."aggregate_metrics" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."aggregate_snapshots" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  metric_id uuid NOT NULL,
  snapshot_key text NOT NULL,
  source_high_watermark text NOT NULL,
  result_ref text NOT NULL,
  result_hash text NOT NULL,
  frozen_at timestamptz NOT NULL,
  UNIQUE ("tenant_id", "snapshot_key")
);

ALTER TABLE "product"."aggregate_snapshots" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."aggregate_snapshots" FORCE ROW LEVEL SECURITY;

CREATE POLICY "aggregate_snapshots_tenant_isolation" ON "product"."aggregate_snapshots" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TRIGGER "aggregate_snapshots_append_only" BEFORE UPDATE OR DELETE ON "product"."aggregate_snapshots" FOR EACH ROW EXECUTE FUNCTION "agent".reject_append_only_mutation();

CREATE TABLE IF NOT EXISTS "product"."aggregate_query_budgets" (
  tenant_id uuid NOT NULL,
  snapshot_id uuid NOT NULL,
  consumed integer NOT NULL DEFAULT 0,
  limit_value integer NOT NULL,
  version bigint NOT NULL DEFAULT 1,
  PRIMARY KEY (tenant_id,snapshot_id)
);

ALTER TABLE "product"."aggregate_query_budgets" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."aggregate_query_budgets" FORCE ROW LEVEL SECURITY;

CREATE POLICY "aggregate_query_budgets_tenant_isolation" ON "product"."aggregate_query_budgets" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."aggregate_suppressions" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  snapshot_id uuid NOT NULL,
  cell_key_hash text NOT NULL,
  suppressed boolean NOT NULL,
  reason text NOT NULL,
  UNIQUE ("tenant_id", "snapshot_id", "cell_key_hash")
);

ALTER TABLE "product"."aggregate_suppressions" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."aggregate_suppressions" FORCE ROW LEVEL SECURITY;

CREATE POLICY "aggregate_suppressions_tenant_isolation" ON "product"."aggregate_suppressions" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TRIGGER "aggregate_suppressions_append_only" BEFORE UPDATE OR DELETE ON "product"."aggregate_suppressions" FOR EACH ROW EXECUTE FUNCTION "agent".reject_append_only_mutation();

CREATE TABLE IF NOT EXISTS "product"."user_preferences" (
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1,
  locale text NOT NULL,
  timezone text NOT NULL,
  coach_preferences jsonb NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id,user_id)
);

ALTER TABLE "product"."user_preferences" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."user_preferences" FORCE ROW LEVEL SECURITY;

CREATE POLICY "user_preferences_tenant_isolation" ON "product"."user_preferences" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."reminder_schedules" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  status text NOT NULL CHECK (status IN ('active','paused','cancelled','completed')),
  timezone text NOT NULL,
  local_schedule jsonb NOT NULL,
  next_occurrence_at timestamptz,
  cancelled_at timestamptz
);

ALTER TABLE "product"."reminder_schedules" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."reminder_schedules" FORCE ROW LEVEL SECURITY;

CREATE POLICY "reminder_schedules_tenant_isolation" ON "product"."reminder_schedules" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."reminder_deliveries" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  schedule_id uuid NOT NULL,
  occurrence_at timestamptz NOT NULL,
  delivery_key text NOT NULL,
  status text NOT NULL,
  attempt_count integer NOT NULL DEFAULT 0,
  delivered_at timestamptz,
  last_error_code text,
  UNIQUE ("tenant_id", "user_id", "schedule_id", "occurrence_at"),
  UNIQUE ("delivery_key")
);

ALTER TABLE "product"."reminder_deliveries" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."reminder_deliveries" FORCE ROW LEVEL SECURITY;

CREATE POLICY "reminder_deliveries_tenant_isolation" ON "product"."reminder_deliveries" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."byok_credentials" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  provider_id text NOT NULL,
  bound_host text NOT NULL,
  secret_ref text NOT NULL,
  secret_version text NOT NULL,
  status text NOT NULL,
  last_validated_at timestamptz,
  UNIQUE ("tenant_id", "user_id", "provider_id", "bound_host")
);

ALTER TABLE "product"."byok_credentials" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."byok_credentials" FORCE ROW LEVEL SECURITY;

CREATE POLICY "byok_credentials_tenant_isolation" ON "product"."byok_credentials" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."memory_policies" (
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1,
  enabled boolean NOT NULL,
  retention_days integer,
  allowed_kinds jsonb NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id,user_id)
);

ALTER TABLE "product"."memory_policies" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."memory_policies" FORCE ROW LEVEL SECURITY;

CREATE POLICY "memory_policies_tenant_isolation" ON "product"."memory_policies" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "product"."data_export_requests" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  status text NOT NULL,
  scope jsonb NOT NULL,
  object_ref text,
  content_hash text,
  expires_at timestamptz,
  completed_at timestamptz
);

ALTER TABLE "product"."data_export_requests" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "product"."data_export_requests" FORCE ROW LEVEL SECURITY;

CREATE POLICY "data_export_requests_tenant_isolation" ON "product"."data_export_requests" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "agent"."events" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  seq bigint NOT NULL,
  event_type text NOT NULL,
  event_schema_version integer NOT NULL,
  aggregate_kind text NOT NULL,
  aggregate_id uuid NOT NULL,
  aggregate_version bigint NOT NULL,
  store_epoch uuid NOT NULL,
  occurred_at timestamptz NOT NULL,
  committed_at timestamptz NOT NULL,
  actor jsonb NOT NULL,
  causation_id uuid,
  correlation_id uuid NOT NULL,
  payload_ref text NOT NULL,
  payload_hash text NOT NULL,
  UNIQUE ("tenant_id", "user_id", "seq"),
  UNIQUE ("tenant_id", "aggregate_kind", "aggregate_id", "aggregate_version")
);

ALTER TABLE "agent"."events" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."events" FORCE ROW LEVEL SECURITY;

CREATE POLICY "events_tenant_isolation" ON "agent"."events" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TRIGGER "events_append_only" BEFORE UPDATE OR DELETE ON "agent"."events" FOR EACH ROW EXECUTE FUNCTION "agent".reject_append_only_mutation();

CREATE TABLE IF NOT EXISTS "agent"."idempotency_responses" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  operation_id text NOT NULL,
  idempotency_key_hash bytea NOT NULL,
  request_hash text NOT NULL,
  request_id text NOT NULL,
  status text NOT NULL CHECK (status IN ('in_progress','completed','failed')),
  response_status integer CHECK (response_status BETWEEN 100 AND 599),
  response_content_type text,
  response_payload_ref text,
  response_hash text,
  resource_version bigint,
  expires_at timestamptz NOT NULL,
  completed_at timestamptz,
  UNIQUE ("tenant_id", "user_id", "operation_id", "idempotency_key_hash"),
  CHECK ((status = 'in_progress' AND completed_at IS NULL) OR (status IN ('completed','failed') AND completed_at IS NOT NULL))
);

ALTER TABLE "agent"."idempotency_responses" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."idempotency_responses" FORCE ROW LEVEL SECURITY;

CREATE POLICY "idempotency_responses_tenant_isolation" ON "agent"."idempotency_responses" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "agent"."event_cursors" (
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  last_seq bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id,user_id)
);

ALTER TABLE "agent"."event_cursors" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."event_cursors" FORCE ROW LEVEL SECURITY;

CREATE POLICY "event_cursors_tenant_isolation" ON "agent"."event_cursors" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "agent"."runs" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  conversation_id uuid NOT NULL,
  status text NOT NULL,
  run_version bigint NOT NULL,
  pending_command_id uuid,
  active_command_id uuid,
  active_attempt_id uuid,
  current_fence bigint NOT NULL DEFAULT 0,
  lease_token_hash bytea,
  lease_expires_at timestamptz,
  cancel_requested_at timestamptz,
  due_at timestamptz NOT NULL,
  profile_snapshot_id text NOT NULL,
  budget_snapshot jsonb NOT NULL
);

ALTER TABLE "agent"."runs" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."runs" FORCE ROW LEVEL SECURITY;

CREATE POLICY "runs_tenant_isolation" ON "agent"."runs" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "agent"."tool_calls" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  run_id uuid NOT NULL,
  status text NOT NULL,
  tool_call_version bigint NOT NULL,
  tool_name text NOT NULL,
  descriptor_snapshot_id text NOT NULL,
  normalized_input_ref text NOT NULL,
  request_hash text NOT NULL,
  effect_class text NOT NULL,
  effect_key text,
  pending_command_id uuid,
  active_command_id uuid,
  active_attempt_id uuid,
  current_fence bigint NOT NULL DEFAULT 0,
  lease_token_hash bytea,
  lease_expires_at timestamptz,
  approval_scope_hash text,
  prepared_revision_id uuid
);

ALTER TABLE "agent"."tool_calls" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."tool_calls" FORCE ROW LEVEL SECURITY;

CREATE POLICY "tool_calls_tenant_isolation" ON "agent"."tool_calls" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "agent"."parallel_groups" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  run_id uuid NOT NULL,
  join_policy text NOT NULL,
  required_count integer NOT NULL,
  joined boolean NOT NULL DEFAULT false,
  continuation_id uuid
);

ALTER TABLE "agent"."parallel_groups" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."parallel_groups" FORCE ROW LEVEL SECURITY;

CREATE POLICY "parallel_groups_tenant_isolation" ON "agent"."parallel_groups" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "agent"."child_groups" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  parent_run_id uuid NOT NULL,
  join_policy text NOT NULL,
  required_count integer NOT NULL,
  joined boolean NOT NULL DEFAULT false,
  continuation_id uuid
);

ALTER TABLE "agent"."child_groups" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."child_groups" FORCE ROW LEVEL SECURITY;

CREATE POLICY "child_groups_tenant_isolation" ON "agent"."child_groups" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "agent"."run_cancellations" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  run_id uuid NOT NULL,
  root_cancellation_id uuid NOT NULL,
  parent_cancellation_id uuid,
  cancel_generation bigint NOT NULL,
  status text NOT NULL,
  requested_by uuid NOT NULL,
  requested_at timestamptz NOT NULL,
  reason text NOT NULL,
  settled_at timestamptz
);

ALTER TABLE "agent"."run_cancellations" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."run_cancellations" FORCE ROW LEVEL SECURITY;

CREATE POLICY "run_cancellations_tenant_isolation" ON "agent"."run_cancellations" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "agent"."continuations" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  run_id uuid NOT NULL,
  run_version bigint NOT NULL,
  group_kind text NOT NULL,
  group_id uuid NOT NULL,
  command_id uuid NOT NULL,
  status text NOT NULL,
  UNIQUE ("tenant_id", "run_id", "run_version", "group_kind", "group_id")
);

ALTER TABLE "agent"."continuations" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."continuations" FORCE ROW LEVEL SECURITY;

CREATE POLICY "continuations_tenant_isolation" ON "agent"."continuations" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "agent"."outbox" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  command_id uuid NOT NULL,
  command_type text NOT NULL,
  aggregate_kind text NOT NULL,
  aggregate_id uuid NOT NULL,
  store_epoch uuid NOT NULL,
  payload_ref text NOT NULL,
  payload_hash text NOT NULL,
  status text NOT NULL,
  available_at timestamptz NOT NULL,
  publisher_lease_hash bytea,
  publisher_lease_expires_at timestamptz,
  publish_attempts integer NOT NULL DEFAULT 0,
  published_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE ("command_id")
);

ALTER TABLE "agent"."outbox" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."outbox" FORCE ROW LEVEL SECURITY;

CREATE POLICY "outbox_tenant_isolation" ON "agent"."outbox" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "agent"."inbox" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  consumer_name text NOT NULL,
  command_id uuid NOT NULL,
  status text NOT NULL,
  owner_attempt_id uuid,
  lease_expires_at timestamptz,
  request_hash text NOT NULL,
  completed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE ("tenant_id", "consumer_name", "command_id")
);

ALTER TABLE "agent"."inbox" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."inbox" FORCE ROW LEVEL SECURITY;

CREATE POLICY "inbox_tenant_isolation" ON "agent"."inbox" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "agent"."jobs" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  command_id uuid NOT NULL,
  queue_class text NOT NULL,
  priority integer NOT NULL,
  status text NOT NULL,
  available_at timestamptz NOT NULL,
  due_at timestamptz,
  UNIQUE ("tenant_id", "command_id")
);

ALTER TABLE "agent"."jobs" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."jobs" FORCE ROW LEVEL SECURITY;

CREATE POLICY "jobs_tenant_isolation" ON "agent"."jobs" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "agent"."job_attempts" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  job_id uuid NOT NULL,
  command_id uuid NOT NULL,
  fence bigint NOT NULL,
  lease_token_hash bytea NOT NULL,
  lease_expires_at timestamptz NOT NULL,
  worker_id text NOT NULL,
  status text NOT NULL,
  started_at timestamptz NOT NULL,
  finished_at timestamptz,
  result_hash text
);

ALTER TABLE "agent"."job_attempts" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."job_attempts" FORCE ROW LEVEL SECURITY;

CREATE POLICY "job_attempts_tenant_isolation" ON "agent"."job_attempts" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TRIGGER "job_attempts_append_only" BEFORE UPDATE OR DELETE ON "agent"."job_attempts" FOR EACH ROW EXECUTE FUNCTION "agent".reject_append_only_mutation();

CREATE TABLE IF NOT EXISTS "agent"."tool_effects" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  effect_scope text NOT NULL,
  provider_id text NOT NULL,
  tool_name text NOT NULL,
  effect_key text NOT NULL,
  request_hash text NOT NULL,
  status text NOT NULL,
  external_resource_ref text,
  reconciliation_due_at timestamptz,
  confirmed_at timestamptz,
  residual_risk_ref text,
  UNIQUE ("tenant_id", "effect_scope", "provider_id", "tool_name", "effect_key")
);

ALTER TABLE "agent"."tool_effects" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."tool_effects" FORCE ROW LEVEL SECURITY;

CREATE POLICY "tool_effects_tenant_isolation" ON "agent"."tool_effects" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "agent"."run_messages" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  run_id uuid NOT NULL,
  role text NOT NULL,
  message_index integer NOT NULL,
  payload_ref text NOT NULL,
  content_hash text NOT NULL,
  finalized_at timestamptz,
  UNIQUE ("tenant_id", "run_id", "message_index")
);

ALTER TABLE "agent"."run_messages" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."run_messages" FORCE ROW LEVEL SECURITY;

CREATE POLICY "run_messages_tenant_isolation" ON "agent"."run_messages" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "agent"."run_message_chunks" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  run_id uuid NOT NULL,
  chunk_seq bigint NOT NULL,
  payload_ref text NOT NULL,
  content_hash text NOT NULL,
  expires_at timestamptz NOT NULL,
  UNIQUE ("tenant_id", "run_id", "chunk_seq")
);

ALTER TABLE "agent"."run_message_chunks" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."run_message_chunks" FORCE ROW LEVEL SECURITY;

CREATE POLICY "run_message_chunks_tenant_isolation" ON "agent"."run_message_chunks" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "agent"."llm_attempts" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  run_id uuid NOT NULL,
  attempt_key text NOT NULL,
  context_manifest jsonb NOT NULL,
  router_snapshot_id text NOT NULL,
  status text NOT NULL,
  total_input_tokens bigint NOT NULL DEFAULT 0,
  total_output_tokens bigint NOT NULL DEFAULT 0,
  total_cost_microunits bigint NOT NULL DEFAULT 0,
  UNIQUE ("tenant_id", "attempt_key")
);

ALTER TABLE "agent"."llm_attempts" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."llm_attempts" FORCE ROW LEVEL SECURITY;

CREATE POLICY "llm_attempts_tenant_isolation" ON "agent"."llm_attempts" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "agent"."llm_provider_attempts" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  llm_attempt_id uuid NOT NULL,
  provider_attempt_id text NOT NULL,
  provider_id text NOT NULL,
  model_id text NOT NULL,
  model_version text,
  provider_request_id text,
  fallback_from_id uuid,
  status text NOT NULL,
  input_tokens bigint NOT NULL DEFAULT 0,
  output_tokens bigint NOT NULL DEFAULT 0,
  cost_microunits bigint NOT NULL DEFAULT 0,
  started_at timestamptz NOT NULL,
  finished_at timestamptz,
  UNIQUE ("tenant_id", "provider_attempt_id")
);

ALTER TABLE "agent"."llm_provider_attempts" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."llm_provider_attempts" FORCE ROW LEVEL SECURITY;

CREATE POLICY "llm_provider_attempts_tenant_isolation" ON "agent"."llm_provider_attempts" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TRIGGER "llm_provider_attempts_append_only" BEFORE UPDATE OR DELETE ON "agent"."llm_provider_attempts" FOR EACH ROW EXECUTE FUNCTION "agent".reject_append_only_mutation();

CREATE TABLE IF NOT EXISTS "agent"."memory_documents" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  memory_kind text NOT NULL,
  source_event_id uuid NOT NULL,
  revision integer NOT NULL,
  payload_ref text NOT NULL,
  content_hash text NOT NULL,
  embedding_ref text,
  status text NOT NULL,
  expires_at timestamptz,
  UNIQUE ("tenant_id", "id", "revision")
);

ALTER TABLE "agent"."memory_documents" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."memory_documents" FORCE ROW LEVEL SECURITY;

CREATE POLICY "memory_documents_tenant_isolation" ON "agent"."memory_documents" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "agent"."snapshots" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  snapshot_kind text NOT NULL,
  aggregate_id uuid NOT NULL,
  aggregate_version bigint NOT NULL,
  state_ref text NOT NULL,
  state_hash text NOT NULL,
  UNIQUE ("tenant_id", "snapshot_kind", "aggregate_id", "aggregate_version")
);

ALTER TABLE "agent"."snapshots" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."snapshots" FORCE ROW LEVEL SECURITY;

CREATE POLICY "snapshots_tenant_isolation" ON "agent"."snapshots" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TRIGGER "snapshots_append_only" BEFORE UPDATE OR DELETE ON "agent"."snapshots" FOR EACH ROW EXECUTE FUNCTION "agent".reject_append_only_mutation();

CREATE TABLE IF NOT EXISTS "agent"."projection_registry" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  projection_name text NOT NULL,
  active_version integer NOT NULL,
  schema_hash text NOT NULL,
  status text NOT NULL,
  UNIQUE ("tenant_id", "projection_name")
);

ALTER TABLE "agent"."projection_registry" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."projection_registry" FORCE ROW LEVEL SECURITY;

CREATE POLICY "projection_registry_tenant_isolation" ON "agent"."projection_registry" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "agent"."projection_checkpoints" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  projection_name text NOT NULL,
  projection_version integer NOT NULL,
  shard text NOT NULL,
  last_seq bigint NOT NULL,
  checksum text,
  status text NOT NULL,
  UNIQUE ("tenant_id", "projection_name", "projection_version", "shard")
);

ALTER TABLE "agent"."projection_checkpoints" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."projection_checkpoints" FORCE ROW LEVEL SECURITY;

CREATE POLICY "projection_checkpoints_tenant_isolation" ON "agent"."projection_checkpoints" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "agent"."approvals" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  run_id uuid,
  tool_call_id uuid,
  approval_kind text NOT NULL,
  proposal_hash text NOT NULL,
  target_version bigint NOT NULL,
  permission_snapshot text NOT NULL,
  status text NOT NULL,
  expires_at timestamptz NOT NULL,
  requested_by uuid NOT NULL
);

ALTER TABLE "agent"."approvals" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."approvals" FORCE ROW LEVEL SECURITY;

CREATE POLICY "approvals_tenant_isolation" ON "agent"."approvals" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "agent"."approval_decisions" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  approval_id uuid NOT NULL,
  actor_user_id uuid NOT NULL,
  session_id uuid NOT NULL,
  decision text NOT NULL,
  proposal_hash text NOT NULL,
  target_version bigint NOT NULL,
  permission_snapshot text NOT NULL,
  reauthenticated_at timestamptz NOT NULL,
  decided_at timestamptz NOT NULL,
  UNIQUE ("tenant_id", "approval_id", "actor_user_id")
);

ALTER TABLE "agent"."approval_decisions" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."approval_decisions" FORCE ROW LEVEL SECURITY;

CREATE POLICY "approval_decisions_tenant_isolation" ON "agent"."approval_decisions" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TRIGGER "approval_decisions_append_only" BEFORE UPDATE OR DELETE ON "agent"."approval_decisions" FOR EACH ROW EXECUTE FUNCTION "agent".reject_append_only_mutation();

CREATE TABLE IF NOT EXISTS "agent"."workspace_revision_commits" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  tool_call_id uuid NOT NULL,
  workspace_id uuid NOT NULL,
  base_revision text NOT NULL,
  prepared_revision text NOT NULL,
  prepared_hash text NOT NULL,
  authorization_event_id uuid,
  effect_key text NOT NULL,
  status text NOT NULL,
  published_revision text,
  confirmed_at timestamptz,
  UNIQUE ("tenant_id", "workspace_id", "effect_key")
);

ALTER TABLE "agent"."workspace_revision_commits" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."workspace_revision_commits" FORCE ROW LEVEL SECURITY;

CREATE POLICY "workspace_revision_commits_tenant_isolation" ON "agent"."workspace_revision_commits" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "agent"."repair_commands" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  repair_kind text NOT NULL,
  target_kind text NOT NULL,
  target_id uuid NOT NULL,
  target_version bigint NOT NULL,
  proposal_hash text NOT NULL,
  evidence_hash text NOT NULL,
  initiator_user_id uuid NOT NULL,
  status text NOT NULL,
  expires_at timestamptz NOT NULL,
  executed_at timestamptz,
  UNIQUE ("tenant_id", "proposal_hash")
);

ALTER TABLE "agent"."repair_commands" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."repair_commands" FORCE ROW LEVEL SECURITY;

CREATE POLICY "repair_commands_tenant_isolation" ON "agent"."repair_commands" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "agent"."repair_approvals" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  repair_command_id uuid NOT NULL,
  approver_user_id uuid NOT NULL,
  session_id uuid NOT NULL,
  proposal_hash text NOT NULL,
  target_version bigint NOT NULL,
  permission_snapshot text NOT NULL,
  reauthenticated_at timestamptz NOT NULL,
  approved_at timestamptz NOT NULL,
  UNIQUE ("tenant_id", "repair_command_id", "approver_user_id")
);

ALTER TABLE "agent"."repair_approvals" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "agent"."repair_approvals" FORCE ROW LEVEL SECURITY;

CREATE POLICY "repair_approvals_tenant_isolation" ON "agent"."repair_approvals" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TRIGGER "repair_approvals_append_only" BEFORE UPDATE OR DELETE ON "agent"."repair_approvals" FOR EACH ROW EXECUTE FUNCTION "agent".reject_append_only_mutation();

CREATE TABLE IF NOT EXISTS "contracts"."contracts" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  contract_number text NOT NULL,
  status text NOT NULL,
  starts_at timestamptz NOT NULL,
  ends_at timestamptz NOT NULL,
  seat_limit integer NOT NULL CHECK (seat_limit > 0),
  region text NOT NULL,
  license_kind text NOT NULL,
  proposal_hash text NOT NULL,
  activated_at timestamptz,
  UNIQUE ("tenant_id", "contract_number")
);

ALTER TABLE "contracts"."contracts" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "contracts"."contracts" FORCE ROW LEVEL SECURITY;

CREATE POLICY "contracts_tenant_isolation" ON "contracts"."contracts" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "contracts"."contract_entitlements" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  contract_id uuid NOT NULL,
  entitlement_key text NOT NULL,
  limit_value bigint,
  config jsonb NOT NULL,
  effective_at timestamptz NOT NULL,
  expires_at timestamptz,
  UNIQUE ("tenant_id", "contract_id", "entitlement_key", "version")
);

ALTER TABLE "contracts"."contract_entitlements" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "contracts"."contract_entitlements" FORCE ROW LEVEL SECURITY;

CREATE POLICY "contract_entitlements_tenant_isolation" ON "contracts"."contract_entitlements" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "contracts"."seat_allocations" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  contract_id uuid NOT NULL,
  membership_id uuid NOT NULL,
  status text NOT NULL,
  allocated_at timestamptz NOT NULL,
  released_at timestamptz,
  UNIQUE ("tenant_id", "contract_id", "membership_id")
);

ALTER TABLE "contracts"."seat_allocations" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "contracts"."seat_allocations" FORCE ROW LEVEL SECURITY;

CREATE POLICY "seat_allocations_tenant_isolation" ON "contracts"."seat_allocations" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "contracts"."credit_buckets" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  contract_id uuid,
  bucket_kind text NOT NULL,
  granted_units bigint NOT NULL CHECK (granted_units >= 0),
  reserved_units bigint NOT NULL DEFAULT 0 CHECK (reserved_units >= 0),
  settled_units bigint NOT NULL DEFAULT 0 CHECK (settled_units >= 0),
  starts_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  CHECK (reserved_units + settled_units <= granted_units)
);

ALTER TABLE "contracts"."credit_buckets" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "contracts"."credit_buckets" FORCE ROW LEVEL SECURITY;

CREATE POLICY "credit_buckets_tenant_isolation" ON "contracts"."credit_buckets" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "contracts"."usage_reservations" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  bucket_id uuid NOT NULL,
  operation_key text NOT NULL,
  reserved_units bigint NOT NULL CHECK (reserved_units >= 0),
  status text NOT NULL,
  expires_at timestamptz NOT NULL,
  settled_at timestamptz,
  released_at timestamptz,
  UNIQUE ("tenant_id", "operation_key")
);

ALTER TABLE "contracts"."usage_reservations" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "contracts"."usage_reservations" FORCE ROW LEVEL SECURITY;

CREATE POLICY "usage_reservations_tenant_isolation" ON "contracts"."usage_reservations" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TABLE IF NOT EXISTS "contracts"."usage_ledger" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  reservation_id uuid,
  entry_kind text NOT NULL,
  units bigint NOT NULL,
  operation_key text NOT NULL,
  causation_id uuid NOT NULL,
  recorded_at timestamptz NOT NULL,
  UNIQUE ("tenant_id", "id"),
  UNIQUE ("tenant_id", "operation_key", "entry_kind")
);

ALTER TABLE "contracts"."usage_ledger" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "contracts"."usage_ledger" FORCE ROW LEVEL SECURITY;

CREATE POLICY "usage_ledger_tenant_isolation" ON "contracts"."usage_ledger" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TRIGGER "usage_ledger_append_only" BEFORE UPDATE OR DELETE ON "contracts"."usage_ledger" FOR EACH ROW EXECUTE FUNCTION "agent".reject_append_only_mutation();

CREATE TABLE IF NOT EXISTS "contracts"."provider_costs" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  provider_attempt_id text NOT NULL,
  provider_id text NOT NULL,
  model_id text NOT NULL,
  input_tokens bigint NOT NULL,
  output_tokens bigint NOT NULL,
  cost_microunits bigint NOT NULL,
  byok boolean NOT NULL,
  recorded_at timestamptz NOT NULL,
  UNIQUE ("tenant_id", "provider_attempt_id")
);

ALTER TABLE "contracts"."provider_costs" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "contracts"."provider_costs" FORCE ROW LEVEL SECURITY;

CREATE POLICY "provider_costs_tenant_isolation" ON "contracts"."provider_costs" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TRIGGER "provider_costs_append_only" BEFORE UPDATE OR DELETE ON "contracts"."provider_costs" FOR EACH ROW EXECUTE FUNCTION "agent".reject_append_only_mutation();

CREATE TABLE IF NOT EXISTS "contracts"."manual_adjustments" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  bucket_id uuid NOT NULL,
  units bigint NOT NULL,
  reason text NOT NULL,
  proposal_hash text NOT NULL,
  approved_event_id uuid NOT NULL,
  actor_user_id uuid NOT NULL,
  recorded_at timestamptz NOT NULL,
  UNIQUE ("tenant_id", "proposal_hash")
);

ALTER TABLE "contracts"."manual_adjustments" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "contracts"."manual_adjustments" FORCE ROW LEVEL SECURITY;

CREATE POLICY "manual_adjustments_tenant_isolation" ON "contracts"."manual_adjustments" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TRIGGER "manual_adjustments_append_only" BEFORE UPDATE OR DELETE ON "contracts"."manual_adjustments" FOR EACH ROW EXECUTE FUNCTION "agent".reject_append_only_mutation();

CREATE TABLE IF NOT EXISTS "contracts"."contract_audit_events" (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  contract_id uuid,
  actor_user_id uuid NOT NULL,
  event_type text NOT NULL,
  reason text NOT NULL,
  before_version bigint,
  after_version bigint,
  proposal_hash text,
  approval_event_ids jsonb NOT NULL,
  occurred_at timestamptz NOT NULL
);

ALTER TABLE "contracts"."contract_audit_events" ENABLE ROW LEVEL SECURITY;

ALTER TABLE "contracts"."contract_audit_events" FORCE ROW LEVEL SECURITY;

CREATE POLICY "contract_audit_events_tenant_isolation" ON "contracts"."contract_audit_events" USING (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid) WITH CHECK (tenant_id = NULLIF(current_setting('lites.tenant_id', true), '')::uuid);

CREATE TRIGGER "contract_audit_events_append_only" BEFORE UPDATE OR DELETE ON "contracts"."contract_audit_events" FOR EACH ROW EXECUTE FUNCTION "agent".reject_append_only_mutation();

ALTER TABLE "identity"."password_credentials" ADD CONSTRAINT "password_credentials_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "identity"."users" ("id") ON DELETE CASCADE;

ALTER TABLE "identity"."email_verifications" ADD CONSTRAINT "email_verifications_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "identity"."users" ("id") ON DELETE CASCADE;

ALTER TABLE "identity"."password_reset_requests" ADD CONSTRAINT "password_reset_requests_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "identity"."users" ("id") ON DELETE CASCADE;

ALTER TABLE "identity"."sessions" ADD CONSTRAINT "sessions_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "identity"."users" ("id") ON DELETE CASCADE;

ALTER TABLE "identity"."sessions" ADD CONSTRAINT "sessions_active_tenant_id_fk" FOREIGN KEY ("active_tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE RESTRICT;

ALTER TABLE "identity"."tenants" ADD CONSTRAINT "tenants_owner_user_id_fk" FOREIGN KEY ("owner_user_id") REFERENCES "identity"."users" ("id") ON DELETE RESTRICT;

ALTER TABLE "identity"."memberships" ADD CONSTRAINT "memberships_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "identity"."memberships" ADD CONSTRAINT "memberships_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "identity"."users" ("id") ON DELETE CASCADE;

ALTER TABLE "identity"."invitations" ADD CONSTRAINT "invitations_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "identity"."invitations" ADD CONSTRAINT "invitations_invited_by_fk" FOREIGN KEY ("invited_by") REFERENCES "identity"."users" ("id") ON DELETE RESTRICT;

ALTER TABLE "identity"."security_events" ADD CONSTRAINT "security_events_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "identity"."consent_records" ADD CONSTRAINT "consent_records_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "identity"."account_erasure_requests" ADD CONSTRAINT "account_erasure_requests_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "identity"."anonymous_subjects" ADD CONSTRAINT "anonymous_subjects_system_tenant_id_fk" FOREIGN KEY ("system_tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE RESTRICT;

ALTER TABLE "identity"."onboarding_sessions" ADD CONSTRAINT "onboarding_sessions_anonymous_subject_id_fk" FOREIGN KEY ("anonymous_subject_id") REFERENCES "identity"."anonymous_subjects" ("id") ON DELETE RESTRICT;

ALTER TABLE "identity"."onboarding_sessions" ADD CONSTRAINT "onboarding_sessions_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "identity"."onboarding_claims" ADD CONSTRAINT "onboarding_claims_anonymous_subject_id_fk" FOREIGN KEY ("anonymous_subject_id") REFERENCES "identity"."anonymous_subjects" ("id") ON DELETE RESTRICT;

ALTER TABLE "identity"."onboarding_claims" ADD CONSTRAINT "onboarding_claims_onboarding_session_id_fk" FOREIGN KEY ("onboarding_session_id") REFERENCES "identity"."onboarding_sessions" ("id") ON DELETE CASCADE;

ALTER TABLE "identity"."onboarding_claims" ADD CONSTRAINT "onboarding_claims_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "identity"."anonymous_erasure_receipts" ADD CONSTRAINT "anonymous_erasure_receipts_claim_id_fk" FOREIGN KEY ("claim_id") REFERENCES "identity"."onboarding_claims" ("id") ON DELETE CASCADE;

ALTER TABLE "identity"."anonymous_erasure_receipts" ADD CONSTRAINT "anonymous_erasure_receipts_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."role_profiles" ADD CONSTRAINT "role_profiles_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."capabilities" ADD CONSTRAINT "capabilities_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."role_capability_requirements" ADD CONSTRAINT "role_capability_requirements_role_profile_id_fk" FOREIGN KEY ("role_profile_id") REFERENCES "product"."role_profiles" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."role_capability_requirements" ADD CONSTRAINT "role_capability_requirements_capability_id_fk" FOREIGN KEY ("capability_id") REFERENCES "product"."capabilities" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."role_capability_requirements" ADD CONSTRAINT "role_capability_requirements_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."transition_templates" ADD CONSTRAINT "transition_templates_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."content_revisions" ADD CONSTRAINT "content_revisions_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."task_templates" ADD CONSTRAINT "task_templates_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."rubric_versions" ADD CONSTRAINT "rubric_versions_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."missions" ADD CONSTRAINT "missions_target_role_profile_id_fk" FOREIGN KEY ("target_role_profile_id") REFERENCES "product"."role_profiles" ("id") ON DELETE RESTRICT;

ALTER TABLE "product"."missions" ADD CONSTRAINT "missions_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."mission_imports" ADD CONSTRAINT "mission_imports_mission_id_fk" FOREIGN KEY ("mission_id") REFERENCES "product"."missions" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."mission_imports" ADD CONSTRAINT "mission_imports_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."mission_focuses" ADD CONSTRAINT "mission_focuses_mission_id_fk" FOREIGN KEY ("mission_id") REFERENCES "product"."missions" ("id") ON DELETE RESTRICT;

ALTER TABLE "product"."mission_focuses" ADD CONSTRAINT "mission_focuses_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."route_revisions" ADD CONSTRAINT "route_revisions_mission_id_fk" FOREIGN KEY ("mission_id") REFERENCES "product"."missions" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."route_revisions" ADD CONSTRAINT "route_revisions_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."capability_claims" ADD CONSTRAINT "capability_claims_mission_id_fk" FOREIGN KEY ("mission_id") REFERENCES "product"."missions" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."capability_claims" ADD CONSTRAINT "capability_claims_capability_id_fk" FOREIGN KEY ("capability_id") REFERENCES "product"."capabilities" ("id") ON DELETE RESTRICT;

ALTER TABLE "product"."capability_claims" ADD CONSTRAINT "capability_claims_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."evidence" ADD CONSTRAINT "evidence_mission_id_fk" FOREIGN KEY ("mission_id") REFERENCES "product"."missions" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."evidence" ADD CONSTRAINT "evidence_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."claim_evidence_links" ADD CONSTRAINT "claim_evidence_links_claim_id_fk" FOREIGN KEY ("claim_id") REFERENCES "product"."capability_claims" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."claim_evidence_links" ADD CONSTRAINT "claim_evidence_links_evidence_id_fk" FOREIGN KEY ("evidence_id") REFERENCES "product"."evidence" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."claim_evidence_links" ADD CONSTRAINT "claim_evidence_links_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."daily_tasks" ADD CONSTRAINT "daily_tasks_mission_id_fk" FOREIGN KEY ("mission_id") REFERENCES "product"."missions" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."daily_tasks" ADD CONSTRAINT "daily_tasks_route_revision_id_fk" FOREIGN KEY ("route_revision_id") REFERENCES "product"."route_revisions" ("id") ON DELETE RESTRICT;

ALTER TABLE "product"."daily_tasks" ADD CONSTRAINT "daily_tasks_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."submissions" ADD CONSTRAINT "submissions_daily_task_id_fk" FOREIGN KEY ("daily_task_id") REFERENCES "product"."daily_tasks" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."submissions" ADD CONSTRAINT "submissions_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."reviews" ADD CONSTRAINT "reviews_submission_id_fk" FOREIGN KEY ("submission_id") REFERENCES "product"."submissions" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."reviews" ADD CONSTRAINT "reviews_rubric_version_id_fk" FOREIGN KEY ("rubric_version_id") REFERENCES "product"."rubric_versions" ("id") ON DELETE RESTRICT;

ALTER TABLE "product"."reviews" ADD CONSTRAINT "reviews_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."projects" ADD CONSTRAINT "projects_mission_id_fk" FOREIGN KEY ("mission_id") REFERENCES "product"."missions" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."projects" ADD CONSTRAINT "projects_accepted_route_revision_id_fk" FOREIGN KEY ("accepted_route_revision_id") REFERENCES "product"."route_revisions" ("id") ON DELETE RESTRICT;

ALTER TABLE "product"."projects" ADD CONSTRAINT "projects_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."project_milestones" ADD CONSTRAINT "project_milestones_project_id_fk" FOREIGN KEY ("project_id") REFERENCES "product"."projects" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."project_milestones" ADD CONSTRAINT "project_milestones_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."project_workspace_bindings" ADD CONSTRAINT "project_workspace_bindings_project_id_fk" FOREIGN KEY ("project_id") REFERENCES "product"."projects" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."project_workspace_bindings" ADD CONSTRAINT "project_workspace_bindings_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."project_test_runs" ADD CONSTRAINT "project_test_runs_project_id_fk" FOREIGN KEY ("project_id") REFERENCES "product"."projects" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."project_test_runs" ADD CONSTRAINT "project_test_runs_milestone_id_fk" FOREIGN KEY ("milestone_id") REFERENCES "product"."project_milestones" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."project_test_runs" ADD CONSTRAINT "project_test_runs_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."artifacts" ADD CONSTRAINT "artifacts_project_id_fk" FOREIGN KEY ("project_id") REFERENCES "product"."projects" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."artifacts" ADD CONSTRAINT "artifacts_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."artifact_revisions" ADD CONSTRAINT "artifact_revisions_artifact_id_fk" FOREIGN KEY ("artifact_id") REFERENCES "product"."artifacts" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."artifact_revisions" ADD CONSTRAINT "artifact_revisions_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."portfolio_exports" ADD CONSTRAINT "portfolio_exports_project_id_fk" FOREIGN KEY ("project_id") REFERENCES "product"."projects" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."portfolio_exports" ADD CONSTRAINT "portfolio_exports_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."share_grants" ADD CONSTRAINT "share_grants_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."programs" ADD CONSTRAINT "programs_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."cohorts" ADD CONSTRAINT "cohorts_program_id_fk" FOREIGN KEY ("program_id") REFERENCES "product"."programs" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."cohorts" ADD CONSTRAINT "cohorts_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."enrollments" ADD CONSTRAINT "enrollments_cohort_id_fk" FOREIGN KEY ("cohort_id") REFERENCES "product"."cohorts" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."enrollments" ADD CONSTRAINT "enrollments_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."role_packs" ADD CONSTRAINT "role_packs_program_id_fk" FOREIGN KEY ("program_id") REFERENCES "product"."programs" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."role_packs" ADD CONSTRAINT "role_packs_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."aggregate_metrics" ADD CONSTRAINT "aggregate_metrics_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."aggregate_snapshots" ADD CONSTRAINT "aggregate_snapshots_metric_id_fk" FOREIGN KEY ("metric_id") REFERENCES "product"."aggregate_metrics" ("id") ON DELETE RESTRICT;

ALTER TABLE "product"."aggregate_snapshots" ADD CONSTRAINT "aggregate_snapshots_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."aggregate_query_budgets" ADD CONSTRAINT "aggregate_query_budgets_snapshot_id_fk" FOREIGN KEY ("snapshot_id") REFERENCES "product"."aggregate_snapshots" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."aggregate_query_budgets" ADD CONSTRAINT "aggregate_query_budgets_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."aggregate_suppressions" ADD CONSTRAINT "aggregate_suppressions_snapshot_id_fk" FOREIGN KEY ("snapshot_id") REFERENCES "product"."aggregate_snapshots" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."aggregate_suppressions" ADD CONSTRAINT "aggregate_suppressions_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."user_preferences" ADD CONSTRAINT "user_preferences_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."reminder_schedules" ADD CONSTRAINT "reminder_schedules_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."reminder_deliveries" ADD CONSTRAINT "reminder_deliveries_schedule_id_fk" FOREIGN KEY ("schedule_id") REFERENCES "product"."reminder_schedules" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."reminder_deliveries" ADD CONSTRAINT "reminder_deliveries_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."byok_credentials" ADD CONSTRAINT "byok_credentials_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."memory_policies" ADD CONSTRAINT "memory_policies_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "product"."data_export_requests" ADD CONSTRAINT "data_export_requests_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."events" ADD CONSTRAINT "events_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."idempotency_responses" ADD CONSTRAINT "idempotency_responses_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."event_cursors" ADD CONSTRAINT "event_cursors_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."runs" ADD CONSTRAINT "runs_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."tool_calls" ADD CONSTRAINT "tool_calls_run_id_fk" FOREIGN KEY ("run_id") REFERENCES "agent"."runs" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."tool_calls" ADD CONSTRAINT "tool_calls_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."parallel_groups" ADD CONSTRAINT "parallel_groups_run_id_fk" FOREIGN KEY ("run_id") REFERENCES "agent"."runs" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."parallel_groups" ADD CONSTRAINT "parallel_groups_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."child_groups" ADD CONSTRAINT "child_groups_parent_run_id_fk" FOREIGN KEY ("parent_run_id") REFERENCES "agent"."runs" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."child_groups" ADD CONSTRAINT "child_groups_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."run_cancellations" ADD CONSTRAINT "run_cancellations_run_id_fk" FOREIGN KEY ("run_id") REFERENCES "agent"."runs" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."run_cancellations" ADD CONSTRAINT "run_cancellations_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."continuations" ADD CONSTRAINT "continuations_run_id_fk" FOREIGN KEY ("run_id") REFERENCES "agent"."runs" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."continuations" ADD CONSTRAINT "continuations_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."outbox" ADD CONSTRAINT "outbox_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."inbox" ADD CONSTRAINT "inbox_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."jobs" ADD CONSTRAINT "jobs_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."job_attempts" ADD CONSTRAINT "job_attempts_job_id_fk" FOREIGN KEY ("job_id") REFERENCES "agent"."jobs" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."job_attempts" ADD CONSTRAINT "job_attempts_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."tool_effects" ADD CONSTRAINT "tool_effects_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."run_messages" ADD CONSTRAINT "run_messages_run_id_fk" FOREIGN KEY ("run_id") REFERENCES "agent"."runs" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."run_messages" ADD CONSTRAINT "run_messages_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."run_message_chunks" ADD CONSTRAINT "run_message_chunks_run_id_fk" FOREIGN KEY ("run_id") REFERENCES "agent"."runs" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."run_message_chunks" ADD CONSTRAINT "run_message_chunks_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."llm_attempts" ADD CONSTRAINT "llm_attempts_run_id_fk" FOREIGN KEY ("run_id") REFERENCES "agent"."runs" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."llm_attempts" ADD CONSTRAINT "llm_attempts_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."llm_provider_attempts" ADD CONSTRAINT "llm_provider_attempts_llm_attempt_id_fk" FOREIGN KEY ("llm_attempt_id") REFERENCES "agent"."llm_attempts" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."llm_provider_attempts" ADD CONSTRAINT "llm_provider_attempts_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."memory_documents" ADD CONSTRAINT "memory_documents_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."snapshots" ADD CONSTRAINT "snapshots_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."projection_registry" ADD CONSTRAINT "projection_registry_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."projection_checkpoints" ADD CONSTRAINT "projection_checkpoints_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."approvals" ADD CONSTRAINT "approvals_run_id_fk" FOREIGN KEY ("run_id") REFERENCES "agent"."runs" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."approvals" ADD CONSTRAINT "approvals_tool_call_id_fk" FOREIGN KEY ("tool_call_id") REFERENCES "agent"."tool_calls" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."approvals" ADD CONSTRAINT "approvals_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."approval_decisions" ADD CONSTRAINT "approval_decisions_approval_id_fk" FOREIGN KEY ("approval_id") REFERENCES "agent"."approvals" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."approval_decisions" ADD CONSTRAINT "approval_decisions_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."workspace_revision_commits" ADD CONSTRAINT "workspace_revision_commits_tool_call_id_fk" FOREIGN KEY ("tool_call_id") REFERENCES "agent"."tool_calls" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."workspace_revision_commits" ADD CONSTRAINT "workspace_revision_commits_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."repair_commands" ADD CONSTRAINT "repair_commands_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."repair_approvals" ADD CONSTRAINT "repair_approvals_repair_command_id_fk" FOREIGN KEY ("repair_command_id") REFERENCES "agent"."repair_commands" ("id") ON DELETE CASCADE;

ALTER TABLE "agent"."repair_approvals" ADD CONSTRAINT "repair_approvals_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "contracts"."contracts" ADD CONSTRAINT "contracts_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "contracts"."contract_entitlements" ADD CONSTRAINT "contract_entitlements_contract_id_fk" FOREIGN KEY ("contract_id") REFERENCES "contracts"."contracts" ("id") ON DELETE CASCADE;

ALTER TABLE "contracts"."contract_entitlements" ADD CONSTRAINT "contract_entitlements_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "contracts"."seat_allocations" ADD CONSTRAINT "seat_allocations_contract_id_fk" FOREIGN KEY ("contract_id") REFERENCES "contracts"."contracts" ("id") ON DELETE CASCADE;

ALTER TABLE "contracts"."seat_allocations" ADD CONSTRAINT "seat_allocations_membership_id_fk" FOREIGN KEY ("membership_id") REFERENCES "identity"."memberships" ("id") ON DELETE RESTRICT;

ALTER TABLE "contracts"."seat_allocations" ADD CONSTRAINT "seat_allocations_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "contracts"."credit_buckets" ADD CONSTRAINT "credit_buckets_contract_id_fk" FOREIGN KEY ("contract_id") REFERENCES "contracts"."contracts" ("id") ON DELETE CASCADE;

ALTER TABLE "contracts"."credit_buckets" ADD CONSTRAINT "credit_buckets_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "contracts"."usage_reservations" ADD CONSTRAINT "usage_reservations_bucket_id_fk" FOREIGN KEY ("bucket_id") REFERENCES "contracts"."credit_buckets" ("id") ON DELETE RESTRICT;

ALTER TABLE "contracts"."usage_reservations" ADD CONSTRAINT "usage_reservations_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "contracts"."usage_ledger" ADD CONSTRAINT "usage_ledger_reservation_id_fk" FOREIGN KEY ("reservation_id") REFERENCES "contracts"."usage_reservations" ("id") ON DELETE RESTRICT;

ALTER TABLE "contracts"."usage_ledger" ADD CONSTRAINT "usage_ledger_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "contracts"."provider_costs" ADD CONSTRAINT "provider_costs_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "contracts"."manual_adjustments" ADD CONSTRAINT "manual_adjustments_bucket_id_fk" FOREIGN KEY ("bucket_id") REFERENCES "contracts"."credit_buckets" ("id") ON DELETE RESTRICT;

ALTER TABLE "contracts"."manual_adjustments" ADD CONSTRAINT "manual_adjustments_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

ALTER TABLE "contracts"."contract_audit_events" ADD CONSTRAINT "contract_audit_events_contract_id_fk" FOREIGN KEY ("contract_id") REFERENCES "contracts"."contracts" ("id") ON DELETE RESTRICT;

ALTER TABLE "contracts"."contract_audit_events" ADD CONSTRAINT "contract_audit_events_tenant_id_fk" FOREIGN KEY ("tenant_id") REFERENCES "identity"."tenants" ("id") ON DELETE CASCADE;

COMMIT;
