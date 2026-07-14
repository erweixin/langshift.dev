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
    OR NEW.commit_command_id IS DISTINCT FROM OLD.commit_command_id OR NEW.authorized_at IS DISTINCT FROM OLD.authorized_at) THEN
    RAISE EXCEPTION 'workspace revision authorization mutation is forbidden';
  END IF;
  IF NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN RAISE EXCEPTION 'workspace revision version must advance exactly once'; END IF;
  IF OLD.status='prepared' AND NEW.status IN ('authorized','abandoned') THEN RETURN NEW; END IF;
  IF OLD.status='authorized' AND NEW.status IN ('publishing','abandoned') THEN
    IF NEW.status='publishing' AND NEW.publish_fence<=OLD.publish_fence THEN RAISE EXCEPTION 'workspace publish fence must advance'; END IF;
    RETURN NEW;
  END IF;
  IF OLD.status='publishing' AND NEW.status='publishing' THEN
    IF NEW.publish_attempt_id IS DISTINCT FROM OLD.publish_attempt_id OR NEW.publish_fence<>OLD.publish_fence
      OR NEW.publish_lease_hash IS DISTINCT FROM OLD.publish_lease_hash OR NEW.publish_lease_expires_at<=OLD.publish_lease_expires_at
      OR NEW.publishing_at IS DISTINCT FROM OLD.publishing_at OR NEW.published_revision IS DISTINCT FROM OLD.published_revision
      OR NEW.published_hash IS DISTINCT FROM OLD.published_hash OR NEW.observed_revision IS DISTINCT FROM OLD.observed_revision
      OR NEW.confirmed_at IS DISTINCT FROM OLD.confirmed_at OR NEW.outcome_unknown_at IS DISTINCT FROM OLD.outcome_unknown_at
      OR NEW.reconciliation_due_at IS DISTINCT FROM OLD.reconciliation_due_at OR NEW.reconciliation_attempts<>OLD.reconciliation_attempts
      OR NEW.failed_at IS DISTINCT FROM OLD.failed_at OR NEW.abandoned_at IS DISTINCT FROM OLD.abandoned_at THEN
      RAISE EXCEPTION 'workspace publish heartbeat may only extend the active lease';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status='publishing' AND NEW.status IN ('confirmed','outcome_unknown','failed') AND NEW.publish_fence=OLD.publish_fence THEN RETURN NEW; END IF;
  IF OLD.status='outcome_unknown' AND NEW.status='outcome_unknown' THEN
    IF NEW.publish_attempt_id IS DISTINCT FROM OLD.publish_attempt_id OR NEW.publish_fence<>OLD.publish_fence
      OR NEW.publish_lease_hash IS DISTINCT FROM OLD.publish_lease_hash OR NEW.publish_lease_expires_at IS DISTINCT FROM OLD.publish_lease_expires_at
      OR NEW.publishing_at IS DISTINCT FROM OLD.publishing_at OR NEW.published_revision IS DISTINCT FROM OLD.published_revision
      OR NEW.published_hash IS DISTINCT FROM OLD.published_hash OR NEW.confirmed_at IS DISTINCT FROM OLD.confirmed_at
      OR NEW.outcome_unknown_at IS DISTINCT FROM OLD.outcome_unknown_at OR NEW.reconciliation_due_at<=OLD.reconciliation_due_at
      OR NEW.reconciliation_attempts<>OLD.reconciliation_attempts+1 OR NEW.failed_at IS DISTINCT FROM OLD.failed_at
      OR NEW.abandoned_at IS DISTINCT FROM OLD.abandoned_at THEN
      RAISE EXCEPTION 'workspace reconciliation deferral may only advance evidence and due time';
    END IF;
    RETURN NEW;
  END IF;
  IF OLD.status='outcome_unknown' AND NEW.status IN ('authorized','confirmed','failed','abandoned') AND NEW.publish_fence=OLD.publish_fence THEN RETURN NEW; END IF;
  RAISE EXCEPTION 'invalid workspace revision transition % -> %',OLD.status,NEW.status;
END
$$;
