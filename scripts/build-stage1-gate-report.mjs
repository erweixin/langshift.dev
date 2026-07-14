import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { readFile, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

const root=resolve(import.meta.dirname,"..");
const sha256=value=>createHash("sha256").update(typeof value==="string"||Buffer.isBuffer(value)?value:JSON.stringify(value)).digest("hex");
const load=async path=>JSON.parse(await readFile(resolve(root,path),"utf8"));
const loadOptional=async path=>{try{return await load(path);}catch{return null;}};
const fileHash=async path=>sha256(await readFile(resolve(root,path)));
const currentCommit=execFileSync("git",["rev-parse","HEAD"],{cwd:root,encoding:"utf8"}).trim();

const snapshot=await load("gate-reports/stage-1/contract-snapshot.json");
const gateStatus=await load("gate-reports/stage-1/gate-status.json");
const schemaLint=await load("gate-reports/stage-1/schema-lint.json");
const databaseSmoke=await load("gate-reports/stage-1/database-smoke.json");
const reviewIssues=await load("gate-reports/stage-1/review-issues.json");
const mainJourneys=await load("contracts/traceability/main-journeys.json");
const requirements=await load("contracts/traceability/requirements.json");
const packet=await loadOptional("gate-reports/stage-1/review-packet.json");
const approvalVerification=await loadOptional("gate-reports/stage-1/approval-verification.json");
const productEvals=await load("product-evals/bilingual-transition-evals.json");
const securityEvals=await load("security-tests/fixed-safety-samples.json");
const profileIds=["coach","route_planner","daily_planner","artifact_builder","evaluator"];
const profileEvals=await Promise.all(profileIds.map(id=>load(`profile-evals/${id}/bilingual-slice.json`)));

const dataset=(name,path,value)=>({name,version:value.datasetVersion,sha256:value.hash??sha256(value.samples),sampleCount:value.samples.length,path});
const datasets=[
  dataset("bilingual-transition","product-evals/bilingual-transition-evals.json",productEvals),
  dataset("fixed-safety","security-tests/fixed-safety-samples.json",securityEvals),
  ...profileEvals.map((value,index)=>dataset(`profile-${profileIds[index]}`,`profile-evals/${profileIds[index]}/bilingual-slice.json`,value))
];
const result=async(id,status,evidencePath,summary)=>({id,status,evidencePath,evidenceSha256:await fileHash(evidencePath),summary});
const packetValid=Boolean(packet&&packet.snapshotId===snapshot.snapshotId&&packet.contentRootSha256===snapshot.contentRootSha256&&/^[a-f0-9]{40}$/.test(packet.sourceCommit??""));
const approvalRoles=new Set(approvalVerification?.results?.map(item=>item.role)??[]);
const approvalValid=Boolean(
  packetValid&&approvalVerification?.status==="passed"&&approvalVerification.snapshotId===snapshot.snapshotId&&
  approvalVerification.contentRootSha256===snapshot.contentRootSha256&&approvalVerification.sourceCommit===packet.sourceCommit&&
  approvalVerification.verifiedApproverCount===5&&approvalVerification.results?.length===5&&
  ["product","frontend","backend","security","qa"].every(role=>approvalRoles.has(role))
);
const results=[
  await result("schema-lint",schemaLint.status,"gate-reports/stage-1/schema-lint.json",`${schemaLint.summary.passed}/${schemaLint.summary.checks} contract checks passed`),
  await result("database-contract-smoke",databaseSmoke.status,"gate-reports/stage-1/database-smoke.json",`${databaseSmoke.results.tableCount} tables; cross-tenant visible rows ${databaseSmoke.results.crossTenantVisibleRows}`),
  await result("traceability",requirements.requirements.every(item=>item.gate?.length)?"passed":"failed","contracts/traceability/requirements.json",`${requirements.requirements.length} top-level requirements traced`),
  await result("main-journeys",mainJourneys.journeys.length===5?"passed":"failed","contracts/traceability/main-journeys.json",`${mainJourneys.journeys.length}/5 required journeys traced`),
  await result("review-issues",reviewIssues.openP0.length+reviewIssues.openP1.length===0?"passed":"failed","gate-reports/stage-1/review-issues.json",`open P0=${reviewIssues.openP0.length}; open P1=${reviewIssues.openP1.length}`),
  await result("accountable-approvals",approvalValid?"passed":"pending",approvalVerification?"gate-reports/stage-1/approval-verification.json":"contracts/reviews/approval-record.schema.json",approvalValid?`${approvalVerification.verifiedApproverCount}/5 accountable approvers verified`:"immutable source commit and five accountable approvals are pending")
];
const failures=[
  ...schemaLint.results.filter(item=>item.status==="failed").map(item=>({id:item.id,severity:"P1",summary:item.details,evidence:"gate-reports/stage-1/schema-lint.json"})),
  ...reviewIssues.openP0.map(item=>({id:item.id,severity:"P0",summary:item.summary??item.title,evidence:"gate-reports/stage-1/review-issues.json"})),
  ...reviewIssues.openP1.map(item=>({id:item.id,severity:"P1",summary:item.summary??item.title,evidence:"gate-reports/stage-1/review-issues.json"}))
];
const approvals=approvalValid?approvalVerification.results:[];
const passed=Boolean(packetValid&&approvalValid&&approvals.length===5&&results.every(item=>item.status==="passed")&&failures.length===0);
const blockers=passed?[]:[
  ...(!packetValid?["immutable source commit and review packet for the exact content root"]:[]),
  ...(!approvalValid?["product, frontend, backend, security and QA approvals bound to the review packet"]:[]),
  ...(failures.length?["all failed technical or P0/P1 review results must be resolved"]:[])
];
const reportBase={
  reportVersion:"1.0.0",stage:1,kind:"stage-gate",status:passed?"passed":failures.length?"failed":"in_progress",generatedAt:new Date().toISOString(),
  acceptanceTarget:{name:"Stage 1 contract freeze",scope:"The immutable product, API, event, state, data, security, Profile, evaluation and pilot contracts required before foundation implementation.",releaseEligible:false},
  environment:{runner:process.env.GITHUB_ACTIONS==="true"?"github-actions":"local",nodeVersion:process.version,databaseImageDigest:databaseSmoke.source.containerImage,applicationImageDigest:null,ciRunId:process.env.GITHUB_RUN_ID??null},
  versions:{databaseSchema:{version:"000001_contract_baseline",ddlSha256:databaseSmoke.source.ddlSha256,verificationSha256:databaseSmoke.source.verificationSha256},configurationSnapshot:{snapshotId:snapshot.snapshotId,contentRootSha256:snapshot.contentRootSha256},testData:datasets.map(({path,...item})=>item)},
  results,failures,
  repairCommits:reviewIssues.resolvedOther.map(item=>({issueId:item.id,commit:item.fixCommit??(packetValid?packet.sourceCommit:null),evidence:item.evidence})),
  sourceCommit:packetValid?packet.sourceCommit:null,evidenceCommit:process.env.GITHUB_SHA?.match(/^[a-f0-9]{40}$/)?.[0]??currentCommit,
  snapshot:{snapshotId:snapshot.snapshotId,contentRootSha256:snapshot.contentRootSha256,reviewPacketHash:packetValid?packet.packetHash:null},
  approvals,blockers
};
const report={...reportBase,reportHash:sha256(reportBase)};
await writeFile(resolve(root,"gate-reports/stage-1/gate-report.json"),`${JSON.stringify(report,null,2)}\n`);
await writeFile(resolve(root,"gate-reports/stage-1/gate-status.json"),`${JSON.stringify({...gateStatus,status:report.status,readiness:passed?"approved_for_stage_2":"technical_contract_ready_for_accountable_review",blockingEvidence:blockers},null,2)}\n`);
console.log(`stage-1 gate report: status=${report.status} results=${results.filter(item=>item.status==="passed").length}/${results.length} blockers=${blockers.length} report=${report.reportHash}`);
