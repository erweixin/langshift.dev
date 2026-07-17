BEGIN;
DROP FUNCTION IF EXISTS product.resolve_support_sla(uuid,text);
DROP TRIGGER IF EXISTS support_case_messages_append_only ON product.support_case_messages;
DROP TRIGGER IF EXISTS support_cases_lifecycle ON product.support_cases;
DROP FUNCTION IF EXISTS product.enforce_support_case_lifecycle();
DROP TABLE IF EXISTS product.support_case_messages;
DROP TABLE IF EXISTS product.support_cases;
COMMIT;
