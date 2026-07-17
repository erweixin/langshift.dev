BEGIN;

CREATE TABLE product.task_packs (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL,
  program_id uuid NOT NULL,
  version bigint NOT NULL DEFAULT 1,
  revision integer NOT NULL,
  name text NOT NULL,
  status text NOT NULL,
  task_template_ids jsonb NOT NULL,
  assignment jsonb NOT NULL,
  published_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  UNIQUE (tenant_id,id),
  UNIQUE (tenant_id,program_id,revision),
  CONSTRAINT task_packs_program_fk FOREIGN KEY (tenant_id,program_id) REFERENCES product.programs(tenant_id,id) ON DELETE RESTRICT,
  CONSTRAINT task_packs_contract CHECK (
    version=1 AND revision>0 AND length(btrim(name)) BETWEEN 1 AND 200
    AND status='published'
    AND jsonb_typeof(task_template_ids)='array' AND jsonb_array_length(task_template_ids) BETWEEN 1 AND 500
    AND jsonb_typeof(assignment)='object'
    AND updated_at=created_at AND published_at=created_at
  )
);

ALTER TABLE product.task_packs ENABLE ROW LEVEL SECURITY;
ALTER TABLE product.task_packs FORCE ROW LEVEL SECURITY;
CREATE POLICY task_packs_tenant_isolation ON product.task_packs
  USING (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid)
  WITH CHECK (tenant_id=NULLIF(current_setting('lites.tenant_id',true),'')::uuid);

CREATE TRIGGER task_packs_append_only BEFORE UPDATE OR DELETE ON product.task_packs
  FOR EACH ROW EXECUTE FUNCTION agent.reject_append_only_mutation();
CREATE INDEX task_packs_program_revision_idx ON product.task_packs(tenant_id,program_id,revision DESC);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='lites_product_service') THEN
    GRANT SELECT,INSERT ON product.task_packs TO lites_product_service;
  END IF;
END
$$;

COMMIT;
