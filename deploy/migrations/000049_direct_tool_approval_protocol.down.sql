DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM agent.tool_proposals)
    OR EXISTS (SELECT 1 FROM agent.parallel_groups WHERE group_kind='approval_direct') THEN
    RAISE EXCEPTION 'direct tool approval rollback requires proposal data removal' USING ERRCODE='55000';
  END IF;
END
$$;

DROP TABLE agent.tool_proposals;

ALTER TABLE agent.parallel_groups
  DROP CONSTRAINT parallel_groups_kind_contract,
  DROP CONSTRAINT parallel_groups_continuation_contract,
  ADD CONSTRAINT parallel_groups_kind_contract CHECK (
    group_kind IN ('execution','approval_preview')
  ),
  ADD CONSTRAINT parallel_groups_continuation_contract CHECK (
    continuation_kind IN ('resume','request_approval')
    AND (group_kind<>'execution' OR continuation_kind='resume')
    AND (group_kind<>'approval_preview' OR continuation_kind='request_approval')
  );
