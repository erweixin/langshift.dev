import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { readFile, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

const root=resolve(import.meta.dirname,"..");
const sha256=value=>createHash("sha256").update(typeof value==="string"||Buffer.isBuffer(value)?value:JSON.stringify(value)).digest("hex");
const load=async path=>JSON.parse(await readFile(resolve(root,path),"utf8"));
const argumentIndex=process.argv.indexOf("--source-commit");
const sourceCommit=argumentIndex>=0?process.argv[argumentIndex+1]:process.env.SOURCE_COMMIT;
if(!/^[a-f0-9]{40}$/.test(sourceCommit??""))throw new Error("Provide the 40-character frozen contract commit with --source-commit or SOURCE_COMMIT");
execFileSync("git",["cat-file","-e",`${sourceCommit}^{commit}`],{cwd:root});
const snapshot=await load("gate-reports/stage-1/contract-snapshot.json");
const committedSnapshot=JSON.parse(execFileSync("git",["show",`${sourceCommit}:gate-reports/stage-1/contract-snapshot.json`],{cwd:root,encoding:"utf8"}));
if(committedSnapshot.snapshotId!==snapshot.snapshotId||committedSnapshot.contentRootSha256!==snapshot.contentRootSha256)throw new Error("The selected commit does not contain the current contract snapshot");
for(const file of snapshot.files){
  const committedBody=execFileSync("git",["show",`${sourceCommit}:${file.path}`],{cwd:root});
  if(sha256(committedBody)!==file.sha256)throw new Error(`Committed contract hash mismatch: ${file.path}`);
}
const schemaLint=await load("gate-reports/stage-1/schema-lint.json");
const databaseSmoke=await load("gate-reports/stage-1/database-smoke.json");
const reviewIssues=await load("gate-reports/stage-1/review-issues.json");
const evidenceFileHash=async path=>sha256(await readFile(resolve(root,path)));
const packetBase={
  packetVersion:"1.0.0",status:"awaiting_accountable_approvals",generatedAt:new Date().toISOString(),
  snapshotId:snapshot.snapshotId,contentRootSha256:snapshot.contentRootSha256,sourceCommit,
  evidence:{
    schemaLint:{path:"gate-reports/stage-1/schema-lint.json",reportHash:schemaLint.reportHash,status:schemaLint.status},
    databaseSmoke:{path:"gate-reports/stage-1/database-smoke.json",sha256:await evidenceFileHash("gate-reports/stage-1/database-smoke.json"),status:databaseSmoke.status},
    reviewIssues:{path:"gate-reports/stage-1/review-issues.json",sha256:await evidenceFileHash("gate-reports/stage-1/review-issues.json"),openP0:reviewIssues.openP0.length,openP1:reviewIssues.openP1.length,openOther:reviewIssues.openOther.length},
    checklist:{path:"contracts/reviews/stage-1-review-checklist.json",sha256:await evidenceFileHash("contracts/reviews/stage-1-review-checklist.json")},
    approvalSchema:{path:"contracts/reviews/approval-record.schema.json",sha256:await evidenceFileHash("contracts/reviews/approval-record.schema.json")}
  },
  requiredApprovals:["product","frontend","backend","security","qa"].map(role=>({role,recordPath:`gate-reports/stage-1/approvals/${role}.json`,status:"missing"})),
  promotionRule:"All five signed approvals must bind sourceCommit, snapshotId, contentRootSha256 and this packetHash. Approval evidence may be committed later without changing the frozen contract root."
};
const packet={...packetBase,packetHash:sha256(packetBase)};
await writeFile(resolve(root,"gate-reports/stage-1/review-packet.json"),`${JSON.stringify(packet,null,2)}\n`);
console.log(`stage-1 review packet created: commit=${sourceCommit} snapshot=${snapshot.snapshotId} packet=${packet.packetHash}`);
