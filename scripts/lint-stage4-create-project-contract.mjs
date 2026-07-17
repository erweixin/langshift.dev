#!/usr/bin/env node
import {createHash} from "node:crypto";
import {readFile} from "node:fs/promises";

const root=new URL("../",import.meta.url);
const read=async path=>readFile(new URL(path,root),"utf8");
const contractPath="contracts/openapi/amendments/v1.20.0/create-project-journey.json";
const contractRaw=await read(contractPath);
const contract=JSON.parse(contractRaw);
const handler=await read("internal/product/api/project_handler.go");
const service=await read("internal/product/postgres/project_test_service.go");
const reconciler=await read("internal/product/postgres/project_test_reconciler.go");
const migration=await read("deploy/migrations/000071_project_test_generation_protocol.up.sql");
const worker=await read("cmd/product-worker/main.go");
const manifest=JSON.parse(await read("deploy/migrations/manifest.json"));
const checks=[];
const check=(name,condition)=>checks.push({name,passed:Boolean(condition)});

check("amendment version",contract.amendmentVersion==="1.20.0");
check("base contract version",contract.baseContractVersion==="1.19.0");
check("versioned additive compatibility",contract.compatibility==="versioned-client-additive");
const expectedOperations=["projects.list.v2","projects.create.v2","projects.update.v2","milestones.create.v2","milestones.transition.v2","projects.workspace.get.v2","projects.workspace.bind.v2","projects.workspace.advance.v2","projects.test.generate.v2","projects.complete.v2"];
check("exact operation inventory",JSON.stringify(Object.keys(contract.operations).sort())===JSON.stringify(expectedOperations.sort()));
for(const operation of Object.values(contract.operations)){
  check(`${operation.method} ${operation.path} authenticated`,operation.authentication==="gateway_signed_session"&&operation.authorization==="tenant_user_owner_scope");
  if(operation.method!=="GET") check(`${operation.method} ${operation.path} mutation controls`,operation.csrf==="required"&&operation.idempotency==="required_encrypted_durable_response");
}
for(const [name,schema] of Object.entries(contract.schemas)){
  if(schema.type==="object") check(`${name} closed`,schema.additionalProperties===false);
}
const testOperation=contract.operations["projects.test.generate.v2"];
const testRequest=contract.schemas.ProjectTestGenerationRequestV2;
check("project test is asynchronous",testOperation.status===202&&contract.schemas.ProjectTestGenerationResponseV2.properties.status.const==="queued");
check("project test exact CAS",["expected_project_version","expected_milestone_version","expected_workspace_binding_version","workspace_revision"].every(field=>testRequest.required.includes(field)));
check("caller cannot assert evaluator outcome",["result","passed","verdict","evidence_id","test_run_id"].every(field=>!(field in testRequest.properties)));
check("test request is closed",testRequest.additionalProperties===false);
check("handler requires exact test media type",handler.includes('application/vnd.lites.project-test-generation.v2+json'));
check("handler exposes test-runs path",handler.includes('case "test-runs"'));
check("handler has no caller result field",!handler.slice(handler.indexOf("func (handler ProjectHandler) generateTest"),handler.indexOf("func (handler ProjectHandler) list")).includes('json:"result"'));
check("service pins project evaluator admission",service.includes("CreateProjectEvaluatorConversationInTx"));
check("service pins evaluator behavior",service.includes("PinnedBehaviorBinding: true")&&service.includes("behavior.Evaluator"));
check("service fences exact workspace",service.includes("ExpectedWorkspaceBindingVersion")&&service.includes("WorkspaceManifestHash"));
check("service writes generation only",!service.includes("RecordTestRun("));
check("reconciler consumes only terminal runs",reconciler.includes("r.status IN ('succeeded','failed','cancelled','expired')"));
check("reconciler parses closed output",reconciler.includes("projecttest.Parse(encoded)"));
check("reconciler records evidence",reconciler.includes("INSERT INTO product.evidence"));
check("reconciler invokes trusted ProjectStore finalization",reconciler.includes("store.RecordTestRun"));
check("reconciler supersedes stale state",reconciler.includes("status='superseded'")&&reconciler.includes("project_state_changed"));
check("worker reconciliation wired",worker.includes("projectTestReconcileLoop")&&worker.includes("ProjectTestReconciler"));
check("migration forces RLS",migration.includes("FORCE ROW LEVEL SECURITY"));
check("migration has terminal lifecycle trigger",migration.includes("project_test_generations_lifecycle")&&migration.includes("terminal project test generation mutation is forbidden"));
check("migration has admission lock",migration.includes("agent.lock_owned_project_evaluation"));
check("migration has tenant reconciler",migration.includes("agent.list_project_test_reconciliation_tenants"));
const latest=manifest.migrations.find(item=>item.version===71);
const digest=value=>createHash("sha256").update(value).digest("hex");
check("migration 71 remains content addressed",latest?.version===71&&latest.name==="project_test_generation_protocol");
check("migration manifest content addressed",latest.up_sha256===digest(migration)&&latest.down_sha256===digest(await read(latest.down)));
check("contract content addressed",digest(contractRaw).length===64);

const failures=checks.filter(item=>!item.passed);
if(failures.length){
  for(const failure of failures) process.stderr.write(`failed: ${failure.name}\n`);
  process.exit(1);
}
process.stdout.write(`passed: ${checks.length}/${checks.length} Stage 4 Create contract checks passed; contract ${digest(contractRaw)}\n`);
