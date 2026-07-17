BEGIN;

CREATE TABLE product.support_cases (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  requester_user_id uuid NOT NULL,
  requester_membership_id uuid NOT NULL,
  reference text NOT NULL,
  version bigint NOT NULL DEFAULT 1,
  category text NOT NULL,
  priority text NOT NULL,
  status text NOT NULL,
  subject_ref text NOT NULL,
  subject_hash text NOT NULL,
  support_tier text NOT NULL,
  response_due_at timestamptz,
  resolution_due_at timestamptz,
  first_responded_at timestamptz,
  resolved_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  UNIQUE (tenant_id,id),
  UNIQUE (reference),
  CONSTRAINT support_cases_tenant_fk FOREIGN KEY (tenant_id) REFERENCES identity.tenants(id) ON DELETE RESTRICT,
  CONSTRAINT support_cases_requester_fk FOREIGN KEY (tenant_id,requester_membership_id) REFERENCES identity.memberships(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT support_cases_contract CHECK (
    reference ~ '^LTS-[0-9]{8}-[0-9A-F]{8}$'
    AND version>0
    AND category IN ('product','security','privacy','contract','availability')
    AND priority IN ('low','normal','high','urgent')
    AND status IN ('open','waiting_on_support','waiting_on_customer','resolved','closed')
    AND length(btrim(subject_ref)) BETWEEN 1 AND 2048
    AND subject_hash ~ '^[0-9a-f]{64}$'
    AND support_tier IN ('community','standard','enterprise','premium')
    AND (response_due_at IS NULL OR response_due_at>created_at)
    AND (resolution_due_at IS NULL OR resolution_due_at>created_at)
    AND (first_responded_at IS NULL OR first_responded_at>=created_at)
    AND (resolved_at IS NULL)=(status NOT IN ('resolved','closed'))
    AND updated_at>=created_at
  )
);

CREATE TABLE product.support_case_messages (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  case_id uuid NOT NULL,
  author_user_id uuid NOT NULL,
  author_membership_id uuid NOT NULL,
  author_kind text NOT NULL,
  body_ref text NOT NULL,
  body_hash text NOT NULL,
  created_at timestamptz NOT NULL,
  UNIQUE (tenant_id,id),
  CONSTRAINT support_messages_case_fk FOREIGN KEY (tenant_id,case_id) REFERENCES product.support_cases(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT support_messages_author_fk FOREIGN KEY (tenant_id,author_membership_id) REFERENCES identity.memberships(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT support_messages_contract CHECK (
    author_kind IN ('customer','tenant_admin','support')
    AND length(btrim(body_ref)) BETWEEN 1 AND 2048
    AND body_hash ~ '^[0-9a-f]{64}$'
  )
);

ALTER TABLE product.support_cases ENABLE ROW LEVEL SECURITY;
ALTER TABLE product.support_cases FORCE ROW LEVEL SECURITY;
CREATE POLICY support_cases_tenant_isolation ON product.support_cases
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);
ALTER TABLE product.support_case_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE product.support_case_messages FORCE ROW LEVEL SECURITY;
CREATE POLICY support_messages_tenant_isolation ON product.support_case_messages
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);

CREATE FUNCTION product.enforce_support_case_lifecycle() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,product AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'support case deletion is forbidden'; END IF;
  IF OLD.status='closed'
    OR NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.requester_user_id IS DISTINCT FROM OLD.requester_user_id
    OR NEW.requester_membership_id IS DISTINCT FROM OLD.requester_membership_id
    OR NEW.reference IS DISTINCT FROM OLD.reference OR NEW.category IS DISTINCT FROM OLD.category
    OR NEW.subject_ref IS DISTINCT FROM OLD.subject_ref OR NEW.subject_hash IS DISTINCT FROM OLD.subject_hash
    OR NEW.support_tier IS DISTINCT FROM OLD.support_tier
    OR NEW.response_due_at IS DISTINCT FROM OLD.response_due_at
    OR NEW.resolution_due_at IS DISTINCT FROM OLD.resolution_due_at
    OR NEW.created_at IS DISTINCT FROM OLD.created_at
    OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at
    OR NOT (
      (OLD.status IN ('open','waiting_on_support') AND NEW.status IN ('waiting_on_support','waiting_on_customer','resolved'))
      OR (OLD.status='waiting_on_customer' AND NEW.status IN ('waiting_on_support','resolved'))
      OR (OLD.status='resolved' AND NEW.status IN ('open','waiting_on_support','closed'))
    )
    OR (OLD.first_responded_at IS NOT NULL AND NEW.first_responded_at IS DISTINCT FROM OLD.first_responded_at)
    OR (OLD.first_responded_at IS NULL AND NEW.first_responded_at IS NOT NULL AND NEW.first_responded_at<OLD.created_at)
  THEN RAISE EXCEPTION 'invalid support case transition'; END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER support_cases_lifecycle BEFORE UPDATE OR DELETE ON product.support_cases
  FOR EACH ROW EXECUTE FUNCTION product.enforce_support_case_lifecycle();
CREATE TRIGGER support_case_messages_append_only BEFORE UPDATE OR DELETE ON product.support_case_messages
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();

CREATE INDEX support_cases_requester_timeline_idx ON product.support_cases(tenant_id,requester_user_id,updated_at DESC,id DESC);
CREATE INDEX support_cases_tenant_timeline_idx ON product.support_cases(tenant_id,updated_at DESC,id DESC);
CREATE INDEX support_messages_case_timeline_idx ON product.support_case_messages(tenant_id,case_id,created_at,id);

CREATE FUNCTION product.resolve_support_sla(p_tenant_id uuid, p_priority text)
RETURNS TABLE(support_tier text,response_minutes integer,resolution_minutes integer)
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path=pg_catalog,contracts SET row_security=off AS $$
DECLARE cfg jsonb; raw_tier text; raw_response text; raw_resolution text;
BEGIN
  IF p_tenant_id IS NULL OR p_priority NOT IN ('low','normal','high','urgent') THEN
    RAISE EXCEPTION 'invalid support SLA query' USING ERRCODE='22023';
  END IF;
  SELECT e.config INTO cfg
  FROM contracts.contracts c
  JOIN LATERAL (
    SELECT config FROM contracts.contract_entitlements
    WHERE tenant_id=c.tenant_id AND contract_id=c.id AND entitlement_key='support_tier'
      AND effective_at<=statement_timestamp() AND COALESCE(expires_at,'infinity'::timestamptz)>statement_timestamp()
    ORDER BY version DESC LIMIT 1
  ) e ON true
  WHERE c.tenant_id=p_tenant_id AND c.status='active'
    AND c.starts_at<=statement_timestamp() AND c.ends_at>statement_timestamp()
  ORDER BY c.version DESC LIMIT 1;

  raw_tier := COALESCE(cfg->>'tier','community');
  IF raw_tier NOT IN ('community','standard','enterprise','premium') THEN raw_tier := 'community'; END IF;
  raw_response := cfg->'response_minutes'->>p_priority;
  raw_resolution := cfg->'resolution_minutes'->>p_priority;
  support_tier := raw_tier;
  response_minutes := CASE WHEN raw_response ~ '^[0-9]{1,7}$' AND raw_response::bigint BETWEEN 5 AND 10080 THEN raw_response::integer ELSE 4320 END;
  resolution_minutes := CASE WHEN raw_resolution ~ '^[0-9]{1,7}$' AND raw_resolution::bigint BETWEEN 30 AND 43200 THEN raw_resolution::integer ELSE NULL END;
  RETURN NEXT;
END
$$;
REVOKE ALL ON FUNCTION product.resolve_support_sla(uuid,text) FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    GRANT SELECT,INSERT,UPDATE ON product.support_cases TO lites_product_service;
    GRANT SELECT,INSERT ON product.support_case_messages TO lites_product_service;
    GRANT EXECUTE ON FUNCTION product.resolve_support_sla(uuid,text) TO lites_product_service;
  END IF;
END
$$;

COMMIT;
