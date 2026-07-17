BEGIN;
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_agent_service') THEN
    REVOKE SELECT ON identity.subject_erasure_tombstones FROM lites_agent_service;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_erasure_worker') THEN
    REVOKE EXECUTE ON FUNCTION identity.list_subject_erasure_tombstones_missing_epoch(uuid,integer),identity.list_subject_erasure_tenants(uuid) FROM lites_erasure_worker;
    REVOKE SELECT,UPDATE ON identity.account_erasure_requests FROM lites_erasure_worker;
    REVOKE SELECT,INSERT ON identity.account_erasure_receipts,identity.subject_erasure_tombstones,identity.security_events FROM lites_erasure_worker;
    REVOKE SELECT,UPDATE ON identity.users,identity.memberships,identity.consent_records FROM lites_erasure_worker;
    REVOKE SELECT,DELETE ON identity.password_credentials,identity.email_verifications,identity.password_reset_requests,identity.sessions,identity.invitations FROM lites_erasure_worker;
    REVOKE SELECT ON identity.onboarding_sessions FROM lites_erasure_worker;
    REVOKE SELECT ON product.missions,product.route_revisions,product.evidence,product.daily_tasks,product.submissions,product.reviews,product.projects,product.artifact_revisions,product.portfolio_exports,product.data_export_requests,product.support_cases,product.support_case_messages FROM lites_erasure_worker;
    REVOKE SELECT ON agent.memory_documents,agent.memory_document_revisions,agent.memory_revision_subjects,agent.memory_revision_derivations,agent.memory_index_projections,agent.retrieval_manifests,agent.retrieval_manifest_chunks,agent.coach_context_snapshots,agent.snapshots,agent.runs,agent.run_messages,agent.tool_calls,agent.idempotency_responses FROM lites_erasure_worker;
    REVOKE SELECT,INSERT,UPDATE ON agent.event_cursors,agent.outbox,agent.inbox FROM lites_erasure_worker;
    REVOKE SELECT,INSERT ON agent.events FROM lites_erasure_worker;
    REVOKE USAGE ON SCHEMA identity,product,agent FROM lites_erasure_worker;
  END IF;
END
$$;
DROP FUNCTION identity.list_subject_erasure_tenants(uuid);
DROP FUNCTION identity.list_subject_erasure_tombstones_missing_epoch(uuid,integer);
DROP TABLE identity.subject_erasure_tombstones;
DROP TABLE identity.account_erasure_receipts;
COMMIT;
