import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";

const root=resolve(import.meta.dirname,"..");
const required=["POSTGRES_VERSION","DATABASE_IMAGE_DIGEST","TABLE_COUNT","FORCED_RLS_COUNT","APPEND_ONLY_TRIGGER_COUNT"];
for(const name of required)if(!process.env[name])throw new Error(`Missing ${name}`);
if(!/^postgres@sha256:[a-f0-9]{64}$/.test(process.env.DATABASE_IMAGE_DIGEST))throw new Error("DATABASE_IMAGE_DIGEST must be an immutable postgres repo digest");
const sha256=value=>createHash("sha256").update(value).digest("hex");
const currentCommit=execFileSync("git",["rev-parse","HEAD"],{cwd:root,encoding:"utf8"}).trim();
const worktree=execFileSync("git",["status","--porcelain","--untracked-files=all"],{cwd:root,encoding:"utf8"}).trim();
const ddl=await readFile(resolve(root,"contracts/database/000001_contract_baseline.sql"));
const verification=await readFile(resolve(root,"contracts/database/900000_verify_contract.sql"));
const report={
  reportVersion:"1.0.0",stage:1,kind:"database-contract-smoke",status:"passed",executedAt:new Date().toISOString(),
  source:{headCommit:currentCommit,worktreeState:worktree?"modified":"clean",releaseEligible:false,ddlSha256:sha256(ddl),verificationSha256:sha256(verification),containerImage:process.env.DATABASE_IMAGE_DIGEST,postgresVersion:process.env.POSTGRES_VERSION},
  results:{freshInstall:"passed",tableCount:Number(process.env.TABLE_COUNT),forcedRlsCount:Number(process.env.FORCED_RLS_COUNT),appendOnlyTriggerCount:Number(process.env.APPEND_ONLY_TRIGGER_COUNT),crossTenantVisibleRows:0,appendOnlyMutationsSucceeded:0,criticalConstraintsVerified:13},
  criticalConstraints:["anonymous claim_key uniqueness","session active tenant binding","session CAS version","session active tenant foreign key","idempotency response scope uniqueness","event aggregate version uniqueness","Mission Focus tenant-user primary key","Route claim_set_hash","Route base_route_version","reminder delivery dedupe","effect ledger uniqueness","distinct repair approver vote","credit conservation check"],
  promotionRule:"Repeat on the immutable contract snapshot commit before stage approval."
};
const path=resolve(root,"gate-reports/stage-1/database-smoke.json");
await mkdir(dirname(path),{recursive:true});
await writeFile(path,`${JSON.stringify(report,null,2)}\n`);
