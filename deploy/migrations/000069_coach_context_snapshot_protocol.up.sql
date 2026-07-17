ALTER TABLE agent.run_messages DROP CONSTRAINT run_messages_scope_contract;
ALTER TABLE agent.run_messages ADD CONSTRAINT run_messages_scope_contract CHECK (
  message_index>=0 AND role IN ('user','assistant','tool')
  AND NULLIF(payload_ref,'') IS NOT NULL
  AND payload_hash ~ '^[0-9a-f]{64}$' AND content_hash ~ '^[0-9a-f]{64}$'
  AND content_type='application/json'
  AND source_kind IN ('conversation_user','product_context','agent_output','tool_result')
  AND trust_label IN ('system_trusted','user_asserted','derived','untrusted_external')
  AND ((role='user' AND source_kind='conversation_user' AND trust_label IN ('user_asserted','untrusted_external'))
    OR (role='user' AND source_kind='product_context' AND trust_label='derived')
    OR (role='assistant' AND source_kind='agent_output' AND trust_label='derived')
    OR (role='tool' AND source_kind='tool_result' AND trust_label IN ('derived','untrusted_external')))
);

CREATE TABLE agent.coach_context_snapshots (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  run_id uuid NOT NULL,
  conversation_id uuid NOT NULL,
  mission_id uuid NOT NULL,
  focus_version bigint NOT NULL CHECK (focus_version>0),
  mission_version bigint NOT NULL CHECK (mission_version>0),
  route_version bigint NOT NULL CHECK (route_version>=0),
  claim_set_hash text NOT NULL,
  route_revision_id uuid,
  route_revision_version bigint,
  daily_task_id uuid,
  daily_task_version bigint,
  preferences_version bigint NOT NULL CHECK (preferences_version>=0),
  manifest jsonb NOT NULL CHECK (jsonb_typeof(manifest)='object'),
  manifest_hash text NOT NULL CHECK (manifest_hash ~ '^[0-9a-f]{64}$'),
  payload_ref text NOT NULL,
  payload_hash text NOT NULL CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
  snapshotted_event_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT coach_context_snapshots_tenant_id_id_unique UNIQUE (tenant_id,id),
  CONSTRAINT coach_context_snapshots_run_unique UNIQUE (tenant_id,run_id),
  CONSTRAINT coach_context_snapshots_route_contract CHECK (
    (route_revision_id IS NULL AND route_revision_version IS NULL)
    OR (route_revision_id IS NOT NULL AND route_revision_version>0)
  ),
  CONSTRAINT coach_context_snapshots_task_contract CHECK (
    (daily_task_id IS NULL AND daily_task_version IS NULL)
    OR (daily_task_id IS NOT NULL AND daily_task_version>0)
  ),
  CONSTRAINT coach_context_snapshots_run_fk FOREIGN KEY (tenant_id,run_id)
    REFERENCES agent.runs(tenant_id,id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT coach_context_snapshots_conversation_fk FOREIGN KEY (tenant_id,conversation_id)
    REFERENCES agent.conversations(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT coach_context_snapshots_event_fk FOREIGN KEY (snapshotted_event_id)
    REFERENCES agent.events(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);

ALTER TABLE agent.coach_context_snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent.coach_context_snapshots FORCE ROW LEVEL SECURITY;
CREATE POLICY coach_context_snapshots_tenant_isolation ON agent.coach_context_snapshots
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);
CREATE TRIGGER coach_context_snapshots_append_only BEFORE UPDATE OR DELETE ON agent.coach_context_snapshots
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();

CREATE FUNCTION agent.read_coach_context_snapshot(
  p_tenant_id uuid,
  p_user_id uuid,
  p_conversation_id uuid
) RETURNS jsonb
LANGUAGE sql
SECURITY DEFINER
SET search_path=pg_catalog,agent,product,identity
SET row_security=off
AS $$
  SELECT jsonb_build_object(
    'schema_version',1,
    'conversation',jsonb_build_object('id',c.id,'version',c.version,'mission_id',c.mission_id,'mode',c.mode),
    'focus',jsonb_build_object('mission_id',f.mission_id,'version',f.focus_version),
    'mission',jsonb_build_object(
      'id',m.id,'version',m.version,'status',m.status,'source_role_profile_id',m.source_role_profile_id,
      'source_role_slug',src.slug,'target_role_profile_id',m.target_role_profile_id,'target_role_slug',target.slug,
      'goal_payload_ref',m.goal_payload_ref,'goal_payload_hash',m.goal_payload_hash,'route_version',m.route_version,
      'claim_set_hash',m.claim_set_hash,'current_route_revision_id',m.current_route_revision_id
    ),
    'route',CASE WHEN r.id IS NULL THEN NULL ELSE jsonb_build_object(
      'id',r.id,'version',r.version,'route_version',r.route_version,'status',r.status,
      'payload_ref',r.route_payload_ref,'payload_hash',r.route_payload_hash,'accepted_at',r.accepted_at
    ) END,
    'daily_task',CASE WHEN task.id IS NULL THEN NULL ELSE jsonb_build_object(
      'id',task.id,'version',task.version,'status',task.status,'practice_kind',task.practice_kind,
      'payload_ref',task.task_payload_ref,'payload_hash',task.task_payload_hash,'estimated_minutes',task.estimated_minutes,
      'difficulty',task.difficulty,'scheduled_for',task.scheduled_for,'focus_version',task.focus_version
    ) END,
    'preferences',jsonb_build_object(
      'version',COALESCE(pref.version,0),'locale',COALESCE(pref.locale,'en'),'timezone',COALESCE(pref.timezone,'UTC'),
      'coach_preferences',COALESCE(pref.coach_preferences,'{"schema_version":1,"difficulty":"standard","available_minutes":45,"tone":"encouraging","explanation_depth":"balanced"}'::jsonb)
    ),
    'claims',COALESCE(claims.items,'[]'::jsonb),
    'evidence',COALESCE(evidence.items,'[]'::jsonb)
  )
  FROM agent.conversations c
  JOIN product.mission_focuses f ON f.tenant_id=c.tenant_id AND f.user_id=c.user_id AND f.mission_id=c.mission_id AND f.focus_version>0
  JOIN product.missions m ON m.tenant_id=c.tenant_id AND m.user_id=c.user_id AND m.id=c.mission_id AND m.status='active'
  LEFT JOIN product.role_profiles src ON src.tenant_id=m.tenant_id AND src.id=m.source_role_profile_id
  JOIN product.role_profiles target ON target.tenant_id=m.tenant_id AND target.id=m.target_role_profile_id
  LEFT JOIN product.route_revisions r ON r.tenant_id=m.tenant_id AND r.user_id=m.user_id AND r.id=m.current_route_revision_id AND r.status='accepted'
  LEFT JOIN product.user_preferences pref ON pref.tenant_id=m.tenant_id AND pref.user_id=m.user_id
  LEFT JOIN LATERAL (
    SELECT t.* FROM product.daily_tasks t
    WHERE t.tenant_id=m.tenant_id AND t.user_id=m.user_id AND t.mission_id=m.id AND t.status<>'rescheduled'
    ORDER BY (t.status IN ('scheduled','in_progress','submitted','reviewing')) DESC,t.scheduled_for DESC,t.created_at DESC,t.id DESC
    LIMIT 1
  ) task ON true
  LEFT JOIN LATERAL (
    SELECT jsonb_agg(jsonb_build_object(
      'id',q.id,'identity_id',q.claim_identity_id,'revision',q.claim_revision,'row_version',q.version,
      'capability_id',q.capability_id,'status',q.status,'origin',q.origin,'verification_level',q.verification_level,
      'statement_ref',q.statement_ref,'recorded_at',q.recorded_at
    ) ORDER BY q.claim_identity_id,q.id) AS items
    FROM (
      SELECT DISTINCT ON (cc.claim_identity_id) cc.* FROM product.capability_claims cc
      WHERE cc.tenant_id=m.tenant_id AND cc.user_id=m.user_id AND cc.mission_id=m.id
      ORDER BY cc.claim_identity_id,cc.claim_revision DESC,cc.id DESC
    ) q
  ) claims ON true
  LEFT JOIN LATERAL (
    SELECT jsonb_agg(jsonb_build_object(
      'id',q.id,'version',q.version,'type',q.evidence_type,'status',q.status,'source_kind',q.source_kind,
      'source_id',q.source_id,'payload_ref',q.payload_ref,'payload_hash',q.content_hash,
      'plaintext_hash',q.plaintext_hash,'recorded_at',q.recorded_at
    ) ORDER BY q.recorded_at DESC,q.id DESC) AS items
    FROM (
      SELECT e.* FROM product.evidence e
      WHERE e.tenant_id=m.tenant_id AND e.user_id=m.user_id AND e.mission_id=m.id
      ORDER BY e.recorded_at DESC,e.id DESC LIMIT 50
    ) q
  ) evidence ON true
  WHERE c.tenant_id=p_tenant_id AND c.user_id=p_user_id AND c.id=p_conversation_id
    AND c.mode='coach' AND c.status='active'
$$;

CREATE FUNCTION agent.lock_coach_context_fence(
  p_tenant_id uuid,
  p_user_id uuid,
  p_conversation_id uuid,
  p_mission_id uuid,
  p_focus_version bigint,
  p_mission_version bigint,
  p_route_version bigint,
  p_claim_set_hash text,
  p_route_revision_id uuid,
  p_route_revision_version bigint,
  p_daily_task_id uuid,
  p_daily_task_version bigint,
  p_preferences_version bigint
) RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,agent,product
SET row_security=off
AS $$
DECLARE
  v_route_id uuid;
  v_route_row_version bigint;
  v_task_id uuid;
  v_task_version bigint;
  v_preferences_version bigint;
BEGIN
  PERFORM 1 FROM agent.conversations c
  JOIN product.mission_focuses f ON f.tenant_id=c.tenant_id AND f.user_id=c.user_id AND f.mission_id=c.mission_id
  JOIN product.missions m ON m.tenant_id=c.tenant_id AND m.user_id=c.user_id AND m.id=c.mission_id
  WHERE c.tenant_id=p_tenant_id AND c.user_id=p_user_id AND c.id=p_conversation_id
    AND c.mode='coach' AND c.status='active' AND c.mission_id=p_mission_id
    AND f.focus_version=p_focus_version AND m.status='active' AND m.version=p_mission_version
    AND m.route_version=p_route_version AND m.claim_set_hash=p_claim_set_hash
    AND m.current_route_revision_id IS NOT DISTINCT FROM p_route_revision_id
  FOR KEY SHARE OF c,f,m;
  IF NOT FOUND THEN RETURN false; END IF;

  IF p_route_revision_id IS NOT NULL THEN
    SELECT r.id,r.version INTO v_route_id,v_route_row_version
    FROM product.route_revisions r
    WHERE r.tenant_id=p_tenant_id AND r.user_id=p_user_id AND r.mission_id=p_mission_id
      AND r.id=p_route_revision_id AND r.status='accepted'
    FOR KEY SHARE;
  END IF;
  IF v_route_id IS DISTINCT FROM p_route_revision_id OR v_route_row_version IS DISTINCT FROM p_route_revision_version THEN RETURN false; END IF;

  SELECT t.id,t.version INTO v_task_id,v_task_version FROM product.daily_tasks t
  WHERE t.tenant_id=p_tenant_id AND t.user_id=p_user_id AND t.mission_id=p_mission_id AND t.status<>'rescheduled'
  ORDER BY (t.status IN ('scheduled','in_progress','submitted','reviewing')) DESC,t.scheduled_for DESC,t.created_at DESC,t.id DESC
  LIMIT 1 FOR KEY SHARE;
  IF v_task_id IS DISTINCT FROM p_daily_task_id OR v_task_version IS DISTINCT FROM p_daily_task_version THEN RETURN false; END IF;

  SELECT p.version INTO v_preferences_version FROM product.user_preferences p
  WHERE p.tenant_id=p_tenant_id AND p.user_id=p_user_id FOR KEY SHARE;
  v_preferences_version := COALESCE(v_preferences_version,0);
  RETURN v_preferences_version=p_preferences_version;
END
$$;

REVOKE ALL ON FUNCTION agent.read_coach_context_snapshot(uuid,uuid,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION agent.lock_coach_context_fence(uuid,uuid,uuid,uuid,bigint,bigint,bigint,text,uuid,bigint,uuid,bigint,bigint) FROM PUBLIC;

DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_agent_control_service') THEN
    GRANT SELECT,INSERT ON agent.coach_context_snapshots TO lites_agent_control_service;
    GRANT EXECUTE ON FUNCTION agent.read_coach_context_snapshot(uuid,uuid,uuid), agent.lock_coach_context_fence(uuid,uuid,uuid,uuid,bigint,bigint,bigint,text,uuid,bigint,uuid,bigint,bigint) TO lites_agent_control_service;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_agent_service') THEN
    GRANT SELECT,INSERT ON agent.coach_context_snapshots TO lites_agent_service;
    GRANT EXECUTE ON FUNCTION agent.read_coach_context_snapshot(uuid,uuid,uuid), agent.lock_coach_context_fence(uuid,uuid,uuid,uuid,bigint,bigint,bigint,text,uuid,bigint,uuid,bigint,bigint) TO lites_agent_service;
  END IF;
END $$;
