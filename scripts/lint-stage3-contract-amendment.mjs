import { createHash } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

const root=resolve(import.meta.dirname,"..");
const sha256=value=>createHash("sha256").update(typeof value==="string"||Buffer.isBuffer(value)?value:JSON.stringify(value)).digest("hex");
const read=path=>readFile(resolve(root,path));
const load=async path=>JSON.parse(await read(path));
const checks=[];
const check=(id,passed,details)=>checks.push({id,status:passed?"passed":"failed",details});

const baseRegistry=await load("contracts/events/registry.json");
const baseFixtures=await load("contracts/events/upcaster-fixtures.json");
const registry=await load("contracts/events/amendments/v1.1.0/registry.json");
const fixtures=await load("contracts/events/amendments/v1.1.0/upcaster-fixtures.json");
const amendment=await load("gate-reports/stage-3/contract-amendment-v1.1.json");
const v12Registry=await load("contracts/events/amendments/v1.2.0/registry.json");
const v12Fixtures=await load("contracts/events/amendments/v1.2.0/upcaster-fixtures.json");
const v12Amendment=await load("gate-reports/stage-3/contract-amendment-v1.2.json");
const v13Registry=await load("contracts/events/amendments/v1.3.0/registry.json");
const v13Fixtures=await load("contracts/events/amendments/v1.3.0/upcaster-fixtures.json");
const v13Amendment=await load("gate-reports/stage-3/contract-amendment-v1.3.json");
const snapshot=await load("gate-reports/stage-1/contract-snapshot.json");
const eventNames=Object.keys(registry.schemas);
const baseNames=new Set(Object.keys(baseRegistry.schemas));

check("AMENDMENT-VERSION",registry.amendmentVersion==="1.1.0"&&registry.baseContractVersion===baseRegistry.contractVersion&&registry.compatibility==="additive","additive event amendment extends the frozen 1.0.0 registry");
check("AMENDMENT-EVENTS",JSON.stringify(eventNames)===JSON.stringify(["AnonymousClaimManualReviewResolved","RepairCommandExpired"])&&eventNames.every(name=>!baseNames.has(name)),"two new event types do not replace a frozen event");
check("AMENDMENT-SCHEMAS",Object.values(registry.schemas).every(schema=>schema.additionalProperties===false&&schema["x-event-schema-version"]===1&&schema.properties.payload.additionalProperties===false&&schema.properties.payload.required.every(field=>field in schema.properties.payload.properties)),"new event envelopes and payloads are closed and versioned");
const resolved=registry.schemas.AnonymousClaimManualReviewResolved.properties.payload.required;
check("CLAIM-REPAIR-EVIDENCE",["claim_id","claim_key","repair_command_id","previous_claim_version","restored_status","destination_commit_event_id","evidence_hash"].every(field=>resolved.includes(field)),"manual claim repair event binds scope, prior version, restored state and evidence");
const expired=registry.schemas.RepairCommandExpired.properties.payload.required;
check("REPAIR-EXPIRY-EVIDENCE",["repair_command_id","target_kind","target_id","target_version","expired_at"].every(field=>expired.includes(field)),"repair expiry event binds target scope and expiry time");
check("AMENDMENT-FIXTURES",fixtures.baseFixtureVersion===baseFixtures.fixtureVersion&&fixtures.fixtures.length===eventNames.length&&fixtures.fixtures.every(fixture=>fixture.fromVersion===1&&fixture.toVersion===1&&fixture.inputHash===sha256(fixture.input)&&fixture.expectedHash===sha256(fixture.expected)&&JSON.stringify(fixture.input)===JSON.stringify(fixture.expected)&&eventNames.includes(fixture.eventType)),"each new event has a deterministic canonical fixture");

const files=[];
for(const path of ["contracts/events/amendments/v1.1.0/registry.json","contracts/events/amendments/v1.1.0/upcaster-fixtures.json"]) files.push({path,sha256:sha256(await read(path))});
const rootHash=sha256({baseSnapshotId:snapshot.snapshotId,files});
check("AMENDMENT-CONTENT-ROOT",JSON.stringify(amendment.files)===JSON.stringify(files)&&amendment.contentRootSha256===rootHash&&amendment.amendmentId===`contract-amendment-${rootHash.slice(0,20)}`,"amendment report binds the exact extension files");
check("AMENDMENT-BASE",amendment.baseSnapshotId===snapshot.snapshotId&&amendment.baseContentRootSha256===snapshot.contentRootSha256,"amendment is anchored to the frozen Stage 1 snapshot");

const v12EventNames=Object.keys(v12Registry.schemas);
check("AMENDMENT-V1.2-VERSION",v12Registry.amendmentVersion==="1.2.0"&&v12Registry.baseContractVersion===registry.amendmentVersion&&v12Registry.compatibility==="additive","v1.2 additively extends the v1.1 Stage 3 amendment");
check("AMENDMENT-V1.2-EVENTS",JSON.stringify(v12EventNames)===JSON.stringify(["ApprovalExpired"])&&v12EventNames.every(name=>!baseNames.has(name)&&!eventNames.includes(name)),"ApprovalExpired is additive and does not replace a frozen event");
check("AMENDMENT-V1.2-SCHEMAS",Object.values(v12Registry.schemas).every(schema=>schema.additionalProperties===false&&schema["x-event-schema-version"]===1&&schema.properties.payload.additionalProperties===false&&schema.properties.payload.required.every(field=>field in schema.properties.payload.properties)),"v1.2 event envelope and payload are closed and versioned");
const approvalExpired=v12Registry.schemas.ApprovalExpired.properties.payload.required;
check("APPROVAL-EXPIRY-EVIDENCE",["approval_id","approval_kind","proposal_hash","target_version","permission_snapshot","expired_at"].every(field=>approvalExpired.includes(field)),"approval expiry binds its immutable authorization scope and expiry time");
check("AMENDMENT-V1.2-FIXTURES",v12Fixtures.baseFixtureVersion===fixtures.fixtureVersion&&v12Fixtures.fixtures.length===v12EventNames.length&&v12Fixtures.fixtures.every(fixture=>fixture.fromVersion===1&&fixture.toVersion===1&&fixture.inputHash===sha256(fixture.input)&&fixture.expectedHash===sha256(fixture.expected)&&JSON.stringify(fixture.input)===JSON.stringify(fixture.expected)&&v12EventNames.includes(fixture.eventType)),"each v1.2 event has a deterministic canonical fixture");
const v12Files=[];
for(const path of ["contracts/events/amendments/v1.2.0/registry.json","contracts/events/amendments/v1.2.0/upcaster-fixtures.json"]) v12Files.push({path,sha256:sha256(await read(path))});
const v12RootHash=sha256({baseAmendmentId:amendment.amendmentId,files:v12Files});
check("AMENDMENT-V1.2-CONTENT-ROOT",JSON.stringify(v12Amendment.files)===JSON.stringify(v12Files)&&v12Amendment.contentRootSha256===v12RootHash&&v12Amendment.amendmentId===`contract-amendment-${v12RootHash.slice(0,20)}`,"v1.2 report binds the exact extension files");
check("AMENDMENT-V1.2-BASE",v12Amendment.baseSnapshotId===snapshot.snapshotId&&v12Amendment.baseContentRootSha256===snapshot.contentRootSha256&&v12Amendment.baseAmendmentId===amendment.amendmentId&&v12Amendment.baseAmendmentContentRootSha256===amendment.contentRootSha256,"v1.2 is anchored to both the frozen Stage 1 snapshot and v1.1 amendment");

const v13EventNames=Object.keys(v13Registry.schemas);
check("AMENDMENT-V1.3-VERSION",v13Registry.amendmentVersion==="1.3.0"&&v13Registry.baseContractVersion===v12Registry.amendmentVersion&&v13Registry.compatibility==="additive","v1.3 additively extends the v1.2 Stage 3 amendment");
check("AMENDMENT-V1.3-EVENTS",JSON.stringify(v13EventNames)===JSON.stringify(["WorkspaceRevisionCommitOutcomeUnknown"])&&v13EventNames.every(name=>!baseNames.has(name)&&!eventNames.includes(name)&&!v12EventNames.includes(name)),"Workspace outcome-unknown is additive and does not replace a frozen event");
const workspaceUnknown=v13Registry.schemas.WorkspaceRevisionCommitOutcomeUnknown.properties.payload.required;
check("WORKSPACE-UNKNOWN-EVIDENCE",["tool_call_id","workspace_id","authorization_id","effect_key","base_revision","prepared_revision","prepared_hash","publish_attempt_id","fence","reconciliation_due_at","observed_revision"].every(field=>workspaceUnknown.includes(field)),"workspace uncertainty binds the immutable revision, authorization, fenced attempt and reconciliation deadline");
check("AMENDMENT-V1.3-FIXTURES",v13Fixtures.baseFixtureVersion===v12Fixtures.fixtureVersion&&v13Fixtures.fixtures.length===1&&v13Fixtures.fixtures.every(fixture=>fixture.inputHash===sha256(fixture.input)&&fixture.expectedHash===sha256(fixture.expected)&&JSON.stringify(fixture.input)===JSON.stringify(fixture.expected)),"v1.3 event has a deterministic canonical fixture");
const v13Files=[];
for(const path of ["contracts/events/amendments/v1.3.0/registry.json","contracts/events/amendments/v1.3.0/upcaster-fixtures.json"]) v13Files.push({path,sha256:sha256(await read(path))});
const v13RootHash=sha256({baseAmendmentId:v12Amendment.amendmentId,files:v13Files});
check("AMENDMENT-V1.3-CONTENT-ROOT",JSON.stringify(v13Amendment.files)===JSON.stringify(v13Files)&&v13Amendment.contentRootSha256===v13RootHash&&v13Amendment.amendmentId===`contract-amendment-${v13RootHash.slice(0,20)}`,"v1.3 report binds the exact extension files");
check("AMENDMENT-V1.3-BASE",v13Amendment.baseSnapshotId===snapshot.snapshotId&&v13Amendment.baseContentRootSha256===snapshot.contentRootSha256&&v13Amendment.baseAmendmentId===v12Amendment.amendmentId&&v13Amendment.baseAmendmentContentRootSha256===v12Amendment.contentRootSha256,"v1.3 is anchored to both the frozen Stage 1 snapshot and v1.2 amendment");

const failures=checks.filter(item=>item.status==="failed");
const reportBase={reportVersion:"1.0.0",stage:3,kind:"contract-amendment-lint",status:failures.length?"failed":"passed",summary:{checks:checks.length,passed:checks.length-failures.length,failed:failures.length},results:checks};
const report={...reportBase,reportHash:sha256(reportBase)};
await writeFile(resolve(root,"gate-reports/stage-3/contract-amendment-lint.json"),`${JSON.stringify(report,null,2)}\n`);
console.log(`${report.status}: ${report.summary.passed}/${report.summary.checks} Stage 3 amendment checks passed; report ${report.reportHash}`);
if(failures.length) process.exitCode=1;
