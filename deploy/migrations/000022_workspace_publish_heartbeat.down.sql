CREATE OR REPLACE FUNCTION agent.enforce_workspace_revision_lifecycle() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'workspace revision deletion is forbidden'; END IF;
  IF OLD.status IN ('confirmed','failed','abandoned') THEN RAISE EXCEPTION 'terminal workspace revision mutation is forbidden'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id OR NEW.created_at IS DISTINCT FROM OLD.created_at
    OR NEW.tool_call_id IS DISTINCT FROM OLD.tool_call_id OR NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
    OR NEW.base_revision IS DISTINCT FROM OLD.base_revision OR NEW.prepared_revision IS DISTINCT FROM OLD.prepared_revision
    OR NEW.prepared_hash IS DISTINCT FROM OLD.prepared_hash OR NEW.effect_key IS DISTINCT FROM OLD.effect_key
    OR NEW.proposal_hash IS DISTINCT FROM OLD.proposal_hash THEN
    RAISE EXCEPTION 'immutable workspace revision scope mutation is forbidden';
  END IF;
  IF OLD.status<>'prepared' AND (NEW.approval_id IS DISTINCT FROM OLD.approval_id OR NEW.approval_version IS DISTINCT FROM OLD.approval_version
    OR NEW.approval_event_id IS DISTINCT FROM OLD.approval_event_id OR NEW.authorization_event_id IS DISTINCT FROM OLD.authorization_event_id
    OR NEW.authorized_at IS DISTINCT FROM OLD.authorized_at) THEN
    RAISE EXCEPTION 'workspace revision authorization mutation is forbidden';
  END IF;
  IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN RAISE EXCEPTION 'workspace revision version must advance exactly once'; END IF;
  IF OLD.status='prepared' AND NEW.status IN ('authorized','abandoned') THEN RETURN NEW; END IF;
  IF OLD.status='authorized' AND NEW.status IN ('publishing','abandoned') THEN
    IF NEW.status='publishing' AND NEW.publish_fence<>OLD.publish_fence+1 THEN RAISE EXCEPTION 'workspace publish fence must advance'; END IF;
    RETURN NEW;
  END IF;
  IF OLD.status='publishing' AND NEW.status IN ('confirmed','outcome_unknown','failed') AND NEW.publish_fence=OLD.publish_fence THEN RETURN NEW; END IF;
  IF OLD.status='outcome_unknown' AND NEW.status IN ('authorized','confirmed','failed','abandoned') AND NEW.publish_fence=OLD.publish_fence THEN RETURN NEW; END IF;
  RAISE EXCEPTION 'invalid workspace revision transition % -> %',OLD.status,NEW.status;
END
$$;
