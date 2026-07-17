DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM product.share_grants
    WHERE user_id=grantee_user_id OR NULLIF(btrim(resource_revision),'') IS NULL
      OR jsonb_typeof(scope)<>'array' OR jsonb_array_length(scope)=0
      OR NOT scope <@ '["read","review"]'::jsonb
      OR expires_at IS NOT NULL AND expires_at<=created_at
      OR revoked_at IS NOT NULL AND revoked_at<created_at
  ) THEN
    RAISE EXCEPTION 'share grant backfill required before migration 75' USING ERRCODE='55000';
  END IF;
  IF EXISTS (
    SELECT 1 FROM product.aggregate_metrics
    WHERE minimum_cell_size<5 OR minimum_complement_size<5
      OR jsonb_typeof(allowed_dimensions)<>'array' OR jsonb_typeof(allowed_time_buckets)<>'array'
  ) OR EXISTS (
    SELECT 1 FROM product.aggregate_query_budgets
    WHERE consumed<0 OR limit_value<1 OR limit_value>100 OR consumed>limit_value OR version<1
  ) OR EXISTS (
    SELECT 1 FROM product.aggregate_snapshots s
    JOIN product.aggregate_metrics m ON m.id=s.metric_id
    WHERE s.tenant_id<>m.tenant_id OR s.result_hash !~ '^[0-9a-f]{64}$'
  ) OR EXISTS (
    SELECT 1 FROM product.aggregate_query_budgets b
    JOIN product.aggregate_snapshots s ON s.id=b.snapshot_id
    WHERE b.tenant_id<>s.tenant_id
  ) OR EXISTS (
    SELECT 1 FROM product.aggregate_suppressions p
    JOIN product.aggregate_snapshots s ON s.id=p.snapshot_id
    WHERE p.tenant_id<>s.tenant_id OR p.cell_key_hash !~ '^[0-9a-f]{64}$'
      OR p.reason NOT IN ('returned','minimum_cell_size','minimum_complement_size')
  ) THEN
    RAISE EXCEPTION 'aggregate privacy backfill required before migration 75' USING ERRCODE='55000';
  END IF;
END
$$;

ALTER TABLE product.share_grants
  ADD CONSTRAINT share_grants_owner_fk FOREIGN KEY (tenant_id,user_id) REFERENCES identity.memberships(tenant_id,user_id) ON DELETE RESTRICT,
  ADD CONSTRAINT share_grants_grantee_fk FOREIGN KEY (tenant_id,grantee_user_id) REFERENCES identity.memberships(tenant_id,user_id) ON DELETE RESTRICT,
  ADD CONSTRAINT share_grants_scope_contract CHECK (
    user_id<>grantee_user_id
    AND resource_kind IN ('conversation','reflection','evidence','workspace','artifact','project','portfolio_export')
    AND NULLIF(btrim(resource_revision),'') IS NOT NULL
    AND jsonb_typeof(scope)='array' AND jsonb_array_length(scope)>0
    AND scope <@ '["read","review"]'::jsonb
    AND (expires_at IS NULL OR expires_at>created_at)
    AND (revoked_at IS NULL OR revoked_at>=created_at)
  );

CREATE UNIQUE INDEX share_grants_exact_scope_unique
  ON product.share_grants(tenant_id,user_id,grantee_user_id,resource_kind,resource_id,resource_revision)
  WHERE revoked_at IS NULL;
CREATE INDEX share_grants_grantee_lookup
  ON product.share_grants(tenant_id,grantee_user_id,resource_kind,resource_id,resource_revision)
  WHERE revoked_at IS NULL;

CREATE FUNCTION product.enforce_share_grant_lifecycle() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, product AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'share grant deletion is forbidden'; END IF;
  IF OLD.revoked_at IS NOT NULL THEN RAISE EXCEPTION 'revoked share grant is immutable'; END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.user_id IS DISTINCT FROM OLD.user_id OR NEW.grantee_user_id IS DISTINCT FROM OLD.grantee_user_id
    OR NEW.resource_kind IS DISTINCT FROM OLD.resource_kind OR NEW.resource_id IS DISTINCT FROM OLD.resource_id
    OR NEW.resource_revision IS DISTINCT FROM OLD.resource_revision OR NEW.scope IS DISTINCT FROM OLD.scope
    OR NEW.expires_at IS DISTINCT FROM OLD.expires_at OR NEW.created_at IS DISTINCT FROM OLD.created_at
    OR NEW.revoked_at IS NULL OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at
  THEN RAISE EXCEPTION 'invalid share grant revocation'; END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER share_grants_lifecycle BEFORE UPDATE OR DELETE ON product.share_grants
FOR EACH ROW EXECUTE FUNCTION product.enforce_share_grant_lifecycle();

ALTER TABLE product.aggregate_metrics
  ADD CONSTRAINT aggregate_metrics_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT aggregate_metrics_privacy_contract CHECK (
    minimum_cell_size>=5 AND minimum_complement_size>=5
    AND jsonb_typeof(allowed_dimensions)='array' AND jsonb_array_length(allowed_dimensions)>0
    AND jsonb_typeof(allowed_time_buckets)='array' AND jsonb_array_length(allowed_time_buckets)>0
    AND allowed_dimensions <@ '["program","cohort","role_pack","locale","coarse_week"]'::jsonb
    AND allowed_time_buckets <@ '["week","month","quarter"]'::jsonb
    AND metric_key IN ('active_members','task_completion_rate','weekly_loop_completion_rate','project_completion_rate','aggregate_credit_usage')
  );
ALTER TABLE product.aggregate_snapshots
  ADD CONSTRAINT aggregate_snapshots_tenant_id_id_unique UNIQUE (tenant_id,id),
  ADD CONSTRAINT aggregate_snapshots_tenant_metric_fk FOREIGN KEY (tenant_id,metric_id)
    REFERENCES product.aggregate_metrics(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT aggregate_snapshots_result_hash_contract CHECK (result_hash ~ '^[0-9a-f]{64}$');
ALTER TABLE product.aggregate_query_budgets
  ADD CONSTRAINT aggregate_query_budgets_tenant_snapshot_fk FOREIGN KEY (tenant_id,snapshot_id)
    REFERENCES product.aggregate_snapshots(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT aggregate_query_budgets_contract CHECK (
    consumed>=0 AND limit_value BETWEEN 1 AND 100 AND consumed<=limit_value AND version>=1
  );
ALTER TABLE product.aggregate_suppressions
  ADD CONSTRAINT aggregate_suppressions_tenant_snapshot_fk FOREIGN KEY (tenant_id,snapshot_id)
    REFERENCES product.aggregate_snapshots(tenant_id,id) ON DELETE RESTRICT,
  ADD CONSTRAINT aggregate_suppressions_privacy_contract CHECK (
    cell_key_hash ~ '^[0-9a-f]{64}$'
    AND reason IN ('returned','minimum_cell_size','minimum_complement_size')
    AND suppressed=(reason<>'returned')
  );

CREATE TABLE product.aggregate_query_audit (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  snapshot_id uuid NOT NULL,
  actor_user_id uuid NOT NULL,
  query_hash text NOT NULL,
  cell_key_hash text,
  decision text NOT NULL CHECK (decision IN ('returned','suppressed','rejected')),
  reason text NOT NULL,
  cell_size integer CHECK (cell_size IS NULL OR cell_size>=0),
  complement_size integer CHECK (complement_size IS NULL OR complement_size>=0),
  budget_before integer NOT NULL CHECK (budget_before>=0),
  budget_after integer NOT NULL CHECK (budget_after>=budget_before),
  occurred_at timestamptz NOT NULL,
  CONSTRAINT aggregate_query_audit_snapshot_fk FOREIGN KEY (tenant_id,snapshot_id)
    REFERENCES product.aggregate_snapshots(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT aggregate_query_audit_actor_fk FOREIGN KEY (tenant_id,actor_user_id)
    REFERENCES identity.memberships(tenant_id,user_id) ON DELETE RESTRICT,
  CONSTRAINT aggregate_query_audit_budget_contract CHECK (budget_after-budget_before BETWEEN 0 AND 1),
  CONSTRAINT aggregate_query_audit_hash_contract CHECK (
    query_hash ~ '^[0-9a-f]{64}$' AND (cell_key_hash IS NULL OR cell_key_hash ~ '^[0-9a-f]{64}$')
  )
);
ALTER TABLE product.aggregate_query_audit ENABLE ROW LEVEL SECURITY;
ALTER TABLE product.aggregate_query_audit FORCE ROW LEVEL SECURITY;
CREATE POLICY aggregate_query_audit_tenant_isolation ON product.aggregate_query_audit
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);
CREATE TRIGGER aggregate_query_audit_append_only BEFORE UPDATE OR DELETE ON product.aggregate_query_audit
FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();
CREATE INDEX aggregate_query_audit_sequence_idx
  ON product.aggregate_query_audit(tenant_id,snapshot_id,actor_user_id,occurred_at,id);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    GRANT USAGE ON SCHEMA identity TO lites_product_service;
    GRANT SELECT ON identity.memberships TO lites_product_service;
    GRANT SELECT,INSERT,UPDATE ON product.share_grants TO lites_product_service;
    GRANT SELECT ON product.aggregate_metrics,product.aggregate_snapshots TO lites_product_service;
    GRANT SELECT,INSERT ON product.aggregate_suppressions TO lites_product_service;
    GRANT SELECT,INSERT,UPDATE ON product.aggregate_query_budgets TO lites_product_service;
    GRANT SELECT,INSERT ON product.aggregate_query_audit TO lites_product_service;
  END IF;
END
$$;
