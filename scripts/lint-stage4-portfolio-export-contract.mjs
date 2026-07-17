#!/usr/bin/env node
import {createHash} from "node:crypto";
import {readFile} from "node:fs/promises";

const root=new URL("../",import.meta.url);
const read=async path=>readFile(new URL(path,root),"utf8");
const raw=await read("contracts/openapi/amendments/v1.21.0/portfolio-export-journey.json");
const contract=JSON.parse(raw);
const handler=await read("internal/product/api/portfolio_handler.go");
const service=await read("internal/product/postgres/portfolio_export_service.go");
const store=await read("internal/product/postgres/portfolio_export_store.go");
const conversation=await read("internal/execution/postgres/conversation_store.go");
const migration=await read("deploy/migrations/000072_portfolio_builder_admission.up.sql");
const main=await read("cmd/product-service/main.go");
const manifest=JSON.parse(await read("deploy/migrations/manifest.json"));
const checks=[];
const check=(name,value)=>checks.push({name,passed:Boolean(value)});
const digest=value=>createHash("sha256").update(value).digest("hex");

check("amendment version",contract.amendmentVersion==="1.21.0"&&contract.baseContractVersion==="1.20.0");
check("operation inventory",JSON.stringify(Object.keys(contract.operations).sort())===JSON.stringify(["portfolio-exports.get.v2","portfolio-exports.request.v2"]));
for(const operation of Object.values(contract.operations)) check(`${operation.method} authenticated`,operation.authentication==="gateway_signed_session"&&operation.authorization==="tenant_user_owner_scope");
const request=contract.schemas.PortfolioExportRequestV2;
check("request closed",request.additionalProperties===false);
check("exact selection and CAS",["project_id","expected_project_version","expected_workspace_binding_version","workspace_revision","artifact_revision_ids","format"].every(field=>request.required.includes(field)));
check("caller provenance forbidden",["content_ref","content_hash","object_ref","object_version","scan_status","scan_result_hash","evidence","result","verdict"].every(field=>!(field in request.properties)));
check("unique bounded revision IDs",request.properties.artifact_revision_ids.uniqueItems===true&&request.properties.artifact_revision_ids.maxItems===100);
check("async response",contract.operations["portfolio-exports.request.v2"].status===202);
check("handler exact media type",handler.includes("application/vnd.lites.portfolio-export.v2+json"));
check("handler rejects unknown fields",handler.includes("DisallowUnknownFields"));
check("service expands artifact evidence",service.includes("product.artifact_revision_evidence")&&service.includes("scanStatus != \"passed\""));
check("service pins builder deployment",service.includes("behavior.ArtifactBuilder")&&service.includes("PinnedBehaviorBinding: true"));
check("service durable idempotency",service.includes("idempotencypostgres.Executor")&&service.includes("RequestInTx"));
check("builder receives exact encrypted input",service.includes("INPUT_MANIFEST=")&&service.includes("artifact_export"));
check("store commits message run",store.includes("AcceptMessageRunInTx")&&store.includes("CreatePortfolioBuilderConversationInTx"));
check("dedicated conversation admission",conversation.includes("conversationAdmissionPortfolioBuilder")&&conversation.includes("agent.lock_owned_portfolio_export"));
check("database admission is completed exact workspace",migration.includes("p.status='completed'")&&migration.includes("w.head_revision=p_workspace_revision")&&migration.includes("SECURITY DEFINER")&&migration.includes("row_security = off"));
check("route wired",main.includes("PortfolioHandler")&&main.includes("/v1/portfolio-exports"));
const entry=manifest.migrations.find(item=>item.version===72);
check("migration 72 manifest",entry?.name==="portfolio_builder_admission"&&entry.up_sha256===digest(migration)&&entry.down_sha256===digest(await read(entry.down)));

const failures=checks.filter(item=>!item.passed);
if(failures.length){for(const failure of failures) process.stderr.write(`failed: ${failure.name}\n`);process.exit(1);}
process.stdout.write(`passed: ${checks.length}/${checks.length} Stage 4 Portfolio export contract checks passed; contract ${digest(raw)}\n`);
