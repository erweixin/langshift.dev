import { createHash, generateKeyPairSync, sign, verify } from "node:crypto";
import { execFileSync } from "node:child_process";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

const root=resolve(import.meta.dirname,"..");
const roles=["product","frontend","backend","security","qa"];
const sha256=value=>createHash("sha256").update(typeof value==="string"||Buffer.isBuffer(value)?value:JSON.stringify(value)).digest("hex");
const canonicalize=value=>{
  if(Array.isArray(value))return `[${value.map(canonicalize).join(",")}]`;
  if(value&&typeof value==="object")return `{${Object.keys(value).sort().map(key=>`${JSON.stringify(key)}:${canonicalize(value[key])}`).join(",")}}`;
  return JSON.stringify(value);
};
const unsignedRecord=record=>Object.fromEntries(Object.entries(record).filter(([key])=>key!=="signature"));
const verifyRecord=(record,keyring)=>{
  if(record.signature?.kind!=="ed25519")return {ok:false,reason:"unsupported_signature_kind"};
  const key=keyring.keys.find(candidate=>candidate.keyId===record.signature.keyId);
  if(!key)return {ok:false,reason:"key_not_found"};
  if(key.status!=="active")return {ok:false,reason:"key_not_active"};
  if(key.approverId!==record.approverId)return {ok:false,reason:"approver_key_mismatch"};
  if(!key.reviewRoles.includes(record.reviewRole))return {ok:false,reason:"role_not_authorized"};
  const ok=verify(null,Buffer.from(canonicalize(unsignedRecord(record))),key.publicKeyPem,Buffer.from(record.signature.value,"base64"));
  return ok?{ok:true,keyId:key.keyId}:{ok:false,reason:"signature_invalid"};
};

if(process.argv.includes("--self-test")){
  const {publicKey,privateKey}=generateKeyPairSync("ed25519");
  const base={snapshotId:"contract-1234567890abcdef1234",contentRootSha256:"a".repeat(64),sourceCommit:"c".repeat(40),reviewPacketHash:"d".repeat(64),reviewRole:"security",approverId:"self-test-security",activeRole:"security-lead",reviewEvidenceHash:"b".repeat(64),decision:"approved",decidedAt:"2026-07-14T00:00:00.000Z"};
  const record={...base,signature:{kind:"ed25519",keyId:"self-test-key",value:sign(null,Buffer.from(canonicalize(base)),privateKey).toString("base64")}};
  const keyring={keys:[{keyId:"self-test-key",approverId:"self-test-security",reviewRoles:["security"],publicKeyPem:publicKey.export({type:"spki",format:"pem"}),status:"active"}]};
  if(!verifyRecord(record,keyring).ok)throw new Error("Valid approval signature was rejected");
  if(verifyRecord({...record,contentRootSha256:"c".repeat(64)},keyring).ok)throw new Error("Tampered approval signature was accepted");
  if(verifyRecord({...record,reviewRole:"qa"},keyring).ok)throw new Error("Unauthorized approval role was accepted");
  console.log("stage-1 approval verifier self-test passed: valid=accepted tampered=rejected wrong-role=rejected");
  process.exit(0);
}

const load=async path=>JSON.parse(await readFile(resolve(root,path),"utf8"));
const loadOptional=async path=>{try{return await load(path);}catch(error){if(error.code==="ENOENT")return null;throw error;}};
const snapshot=await load("gate-reports/stage-1/contract-snapshot.json");
const packet=await loadOptional("gate-reports/stage-1/review-packet.json");
const keyring=await loadOptional("gate-reports/stage-1/approver-keyring.json");
const currentCommit=execFileSync("git",["rev-parse","HEAD"],{cwd:root,encoding:"utf8"}).trim();
const failures=[];
if(!packet)failures.push("missing immutable Stage 1 review packet");
if(!keyring)failures.push("missing accountable approver public-key ring");
if(!packet||!keyring){
  for(const role of roles)failures.push(`missing ${role} approval`);
  const reportBase={reportVersion:"1.0.0",stage:1,kind:"approval-verification",generatedAt:new Date().toISOString(),snapshotId:snapshot.snapshotId,contentRootSha256:snapshot.contentRootSha256,sourceCommit:packet?.sourceCommit??null,currentCommit,verifiedApproverCount:0,results:[],status:"failed",failures};
  const report={...reportBase,reportHash:sha256(reportBase)};
  await mkdir(resolve(root,"gate-reports/stage-1"),{recursive:true});
  await writeFile(resolve(root,"gate-reports/stage-1/approval-verification.json"),`${JSON.stringify(report,null,2)}\n`);
  console.error(`failed: 0/${roles.length} role approvals verified; report ${report.reportHash}`);
  for(const failure of failures)console.error(failure);
  process.exit(1);
}
if(packet.snapshotId!==snapshot.snapshotId||packet.contentRootSha256!==snapshot.contentRootSha256)failures.push("review packet does not match current contract snapshot");
try{
  execFileSync("git",["cat-file","-e",`${packet.sourceCommit}^{commit}`],{cwd:root});
  const committedSnapshot=JSON.parse(execFileSync("git",["show",`${packet.sourceCommit}:gate-reports/stage-1/contract-snapshot.json`],{cwd:root,encoding:"utf8"}));
  if(committedSnapshot.snapshotId!==packet.snapshotId||committedSnapshot.contentRootSha256!==packet.contentRootSha256)failures.push("source commit snapshot does not match review packet");
  for(const file of snapshot.files){const committedBody=execFileSync("git",["show",`${packet.sourceCommit}:${file.path}`],{cwd:root});if(sha256(committedBody)!==file.sha256){failures.push(`source commit contract hash mismatch: ${file.path}`);break;}}
}catch{failures.push("review packet source commit cannot reproduce the frozen contract tree");}
const results=[];
const approverIds=new Set();
for(const role of roles){
  let record;
  try{record=await load(`gate-reports/stage-1/approvals/${role}.json`);}catch{failures.push(`missing ${role} approval`);continue;}
  const structural=record.snapshotId===snapshot.snapshotId&&record.contentRootSha256===snapshot.contentRootSha256&&record.sourceCommit===packet.sourceCommit&&record.reviewPacketHash===packet.packetHash&&record.reviewRole===role&&["approved","rejected"].includes(record.decision)&&/^[a-f0-9]{64}$/.test(record.reviewEvidenceHash??"")&&!Number.isNaN(Date.parse(record.decidedAt));
  if(!structural){failures.push(`${role} approval structure or snapshot binding is invalid`);continue;}
  const signatureResult=verifyRecord(record,keyring);
  if(!signatureResult.ok){failures.push(`${role} approval ${signatureResult.reason}`);continue;}
  if(record.decision!=="approved"){
    failures.push(`${role} review decision is rejected`);
    results.push({role,approverId:record.approverId,keyId:signatureResult.keyId,reviewEvidenceHash:record.reviewEvidenceHash,decidedAt:record.decidedAt,status:"rejected"});
    continue;
  }
  approverIds.add(record.approverId);
  results.push({role,approverId:record.approverId,keyId:signatureResult.keyId,reviewEvidenceHash:record.reviewEvidenceHash,decidedAt:record.decidedAt,status:"verified"});
}
const reportBase={reportVersion:"1.0.0",stage:1,kind:"approval-verification",generatedAt:new Date().toISOString(),snapshotId:snapshot.snapshotId,contentRootSha256:snapshot.contentRootSha256,sourceCommit:packet.sourceCommit,currentCommit,verifiedApproverCount:approverIds.size,results,status:failures.length?"failed":"passed",failures};
const report={...reportBase,reportHash:sha256(reportBase)};
await mkdir(resolve(root,"gate-reports/stage-1"),{recursive:true});
await writeFile(resolve(root,"gate-reports/stage-1/approval-verification.json"),`${JSON.stringify(report,null,2)}\n`);
console.log(`${report.status}: ${results.length}/${roles.length} role approvals verified; report ${report.reportHash}`);
if(failures.length){for(const failure of failures)console.error(failure);process.exitCode=1;}
