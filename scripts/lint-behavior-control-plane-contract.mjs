import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

const root=resolve(import.meta.dirname,"..");
const sha256=value=>createHash("sha256").update(typeof value==="string"||Buffer.isBuffer(value)?value:JSON.stringify(value)).digest("hex");
const read=path=>readFile(resolve(root,path));
const load=async path=>JSON.parse(await read(path));
const contractPath="contracts/openapi/amendments/v1.10.0/behavior-control-plane.json";
const contract=await load(contractPath);
const base=await load("gate-reports/stage-3/contract-amendment-v1.9.json");
const snapshot=await load("gate-reports/stage-1/contract-snapshot.json");
const checks=[];
const check=(id,passed,details)=>checks.push({id,status:passed?"passed":"failed",details});

check("VERSION-CHAIN",contract.amendmentVersion==="1.10.0"&&contract.baseContractVersion==="1.9.0"&&contract.kind==="openapi-operation-extension"&&contract.compatibility==="versioned-server-additive","v1.10 is an additive API extension of the v1.9 behavior-bound Run contract");
const operations=contract.operations??{};
const expectedOperations=["behavior.snapshots.create","behavior.evaluations.record","behavior.promotions.create","behavior.channels.current","behavior.rollbacks.automatic"];
check("OPERATION-SET",JSON.stringify(Object.keys(operations))===JSON.stringify(expectedOperations),"the frozen operation set contains snapshot, evaluation, promotion, current channel and automatic rollback only");
for(const name of expectedOperations.slice(0,3)) {
  const operation=operations[name];
  check(`ADMIN-${name.split(".")[1].toUpperCase()}`,operation?.method==="POST"&&operation.authentication==="gateway_signed_session"&&JSON.stringify(operation.authorization)===JSON.stringify(["owner","admin"])&&operation.csrf==="required"&&operation.idempotency==="required_encrypted_durable_response","admin mutation requires signed session, owner/admin, CSRF and durable idempotency");
}
const promotion=operations["behavior.promotions.create"];
check("PROMOTION-REAUTH",promotion?.reauthentication==="required_within_15_minutes"&&promotion.approvalPolicy?.includes("risk_owner")&&promotion.approvalPolicy?.includes("release_owner")&&promotion.concurrency?.includes("strictly monotonic"),"promotion binds fresh reauthentication, dual control and serialized channel sequence");
const current=operations["behavior.channels.current"];
check("CURRENT-CHANNEL",current?.method==="GET"&&current.path==="/v1/admin/behavior/channels/{profile}/{environment}"&&current.cachePolicy==="no-store"&&current.parameters?.length===2,"current channel is an authenticated non-cacheable profile/environment read");
const rollback=operations["behavior.rollbacks.automatic"];
check("ROLLBACK-BOUNDARY",rollback?.path==="/internal/v1/behavior/rollbacks"&&rollback.authentication==="mutual_tls_exact_spiffe_identity"&&JSON.stringify(rollback.authorization)===JSON.stringify(["behavior_rollback_controller"])&&rollback.networkBoundary?.includes("Dedicated listener")&&rollback.signaturePolicy?.includes("rollback_automation"),"automatic rollback is isolated on an exact-SPIFFE internal listener with a purpose-bound signing key");

const schemas=contract.schemas??{};
const unresolved=[];
const visit=value=>{
  if(Array.isArray(value)) return value.forEach(visit);
  if(!value||typeof value!=="object") return;
  if(typeof value.$ref==="string"&&value.$ref.startsWith("#/schemas/")&&!schemas[value.$ref.slice(10)]) unresolved.push(value.$ref);
  Object.values(value).forEach(visit);
};
visit(contract);
check("SCHEMA-REFS",unresolved.length===0,`all local schema refs resolve (${unresolved.join(",")||"none missing"})`);
const objectSchemas=Object.entries(schemas).filter(([,schema])=>schema.type==="object");
check("CLOSED-OBJECTS",objectSchemas.every(([,schema])=>schema.additionalProperties===false&&schema.required?.every(field=>schema.properties?.[field])),"every named object rejects undeclared fields and declares every required property");
const requestSchemas=["BehaviorSnapshotCreateRequestV1","BehaviorEvaluationCreateRequestV1","BehaviorPromotionCreateRequestV1","BehaviorRollbackCreateRequestV1"];
check("REQUEST-ENVELOPES",requestSchemas.every(name=>schemas[name]?.additionalProperties===false&&schemas[name].required?.includes("request_id")),"all mutation envelopes are closed and bind a client request id");
check("NO-CLIENT-AUTHORITY",contract.forbiddenClientAuthorityFields?.every(field=>requestSchemas.every(name=>!schemas[name].properties?.[field])),"user, session, role, CSRF and automation identity can never be supplied in request envelopes");
const manifest=schemas.BehaviorManifestV1;
check("IMMUTABLE-MANIFEST",["model","prompt","tools","profile_definition","guardrail_policy","router_policy","source_commit"].every(field=>manifest.required?.includes(field))&&manifest.properties?.tools?.maxItems===256,"manifest binds every model-controlled component and source commit");
const evaluationReportSchema=schemas.BehaviorEvaluationReportV1;
check("BILINGUAL-EVALUATION",evaluationReportSchema.properties?.slices?.minItems===2&&evaluationReportSchema.properties?.slices?.maxItems===2&&schemas.BehaviorSliceResultV1.properties?.language?.enum?.includes("en")&&schemas.BehaviorSliceResultV1.properties?.language?.enum?.includes("zh-CN")&&schemas.BehaviorSliceResultV1.properties?.sample_count?.minimum===100,"evaluation evidence requires exactly two independently powered English and Chinese slices");
const zero=schemas.BehaviorZeroToleranceV1;
check("ZERO-TOLERANCE",["safety_failures","authorization_failures","secret_or_pii_leaks","unsupported_confirmed_claims","hallucinated_context_refs","direct_model_capability_writes","unsafe_unapproved_writes"].every(field=>zero.required?.includes(field)),"evaluation report records every frozen zero-tolerance class");
const promotionSchema=schemas.BehaviorPromotionRequestV1;
check("PROMOTION-EVIDENCE",["candidate_snapshot_id","previous_snapshot_id","manifest_hash","evaluation_report_hash","rollout","auto_rollback","requested_at","approvals"].every(field=>promotionSchema.required?.includes(field))&&promotionSchema.properties?.approvals?.minItems===2&&promotionSchema.properties?.approvals?.maxItems===2,"promotion request binds immutable candidate/baseline/evaluation evidence and exactly two approvals");
check("ROLLOUT-GUARD",schemas.BehaviorRolloutPolicyV1.properties?.observation_seconds?.minimum===60&&schemas.BehaviorAutoRollbackPolicyV1.properties?.zero_tolerance_enabled?.const===true&&schemas.BehaviorAutoRollbackPolicyV1.properties?.maximum_error_rate?.maximum===0.05,"rollout always carries observation and bounded automatic rollback thresholds");
const rollbackSchema=schemas.BehaviorRollbackRequestV1;
check("ROLLBACK-EVIDENCE",["from_snapshot_id","to_snapshot_id","trigger","observed_value","threshold","incident_evidence_hash","automation_key_id","occurred_at","signature"].every(field=>rollbackSchema.required?.includes(field))&&rollbackSchema.properties?.sequence?.minimum===2,"rollback request binds prior/target state, breached measurement, incident hash, key and signature");
const result=schemas.BehaviorMutationResultV1;
check("MUTATION-RESULT",result.additionalProperties===false&&["id","resource_id","event_id","hash","replayed"].every(field=>result.required?.includes(field))&&result.properties?.hash?.$ref==="#/schemas/BehaviorDigest","mutation response binds durable resource, event, hash and replay state");
const channel=schemas.BehaviorChannelBindingV1;
check("CHANNEL-BINDING",["channel_id","sequence","snapshot_id","profile","environment","activated_at"].every(field=>channel.required?.includes(field)),"channel read returns the complete immutable deployment binding consumed by new Runs");
check("ENCRYPTED-PAYLOADS",expectedOperations.slice(0,3).every(name=>operations[name].payloadPolicy?.toLowerCase().includes("encrypt"))&&rollback.idempotency?.includes("encrypted"),"event and idempotency response payloads are encrypted before object storage");
check("ERROR-CATALOG",["authentication_required","permission_denied","reauthentication_required","validation_failed","resource_not_found","state_conflict","idempotency_conflict","dependency_unavailable"].every(code=>contract.commonErrors?.includes(code)),"control-plane failure modes map to the frozen public error catalog");

const file={path:contractPath,sha256:sha256(await read(contractPath))};
const contentRootSha256=sha256({baseAmendmentId:base.amendmentId,files:[file]});
const amendment={
  amendmentVersion:"1.10.0",amendmentId:`contract-amendment-${contentRootSha256.slice(0,20)}`,status:"draft",generatedAt:"2026-07-15T00:00:00.000Z",
  baseSnapshotId:snapshot.snapshotId,baseContentRootSha256:snapshot.contentRootSha256,baseAmendmentId:base.amendmentId,baseAmendmentContentRootSha256:base.contentRootSha256,
  contentRootSha256,files:[file],reason:"Expose immutable behavior snapshots, bilingual evaluation evidence, purpose-separated dual-control promotion, exact channel reads and signed automatic rollback through production control-plane boundaries.",
  activationRule:"Backend, security, SRE and QA approval must bind this root after HTTP boundary tests, PostgreSQL exact-replay tests, key-purpose/window checks, migration smoke and deployment isolation verification."
};
check("CONTENT-ROOT",amendment.baseAmendmentId===base.amendmentId&&amendment.baseAmendmentContentRootSha256===base.contentRootSha256&&amendment.contentRootSha256===contentRootSha256,"v1.10 content root is anchored to the exact v1.9 amendment and contract file");

const failures=checks.filter(item=>item.status==="failed");
const reportBase={reportVersion:"1.0.0",stage:3,kind:"behavior-control-plane-contract-lint",status:failures.length?"failed":"passed",summary:{checks:checks.length,passed:checks.length-failures.length,failed:failures.length},results:checks,amendmentId:amendment.amendmentId,contentRootSha256};
const report={...reportBase,reportHash:sha256(reportBase)};
await mkdir(resolve(root,"gate-reports/stage-3"),{recursive:true});
await writeFile(resolve(root,"gate-reports/stage-3/contract-amendment-v1.10.json"),`${JSON.stringify(amendment,null,2)}\n`);
await writeFile(resolve(root,"gate-reports/stage-3/behavior-control-plane-contract-lint.json"),`${JSON.stringify(report,null,2)}\n`);
console.log(`${report.status}: ${report.summary.passed}/${report.summary.checks} behavior control-plane contract checks passed; report ${report.reportHash}`);
if(failures.length) process.exitCode=1;
