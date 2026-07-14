import { createHash, generateKeyPairSync, sign, verify } from "node:crypto";
import { access, mkdir, readFile, writeFile } from "node:fs/promises";
import { relative, resolve } from "node:path";

const root=resolve(import.meta.dirname,"..");
const roles=["product","frontend","backend","security","qa"];
const sha256=value=>createHash("sha256").update(value).digest("hex");
const canonicalize=value=>{
  if(Array.isArray(value))return `[${value.map(canonicalize).join(",")}]`;
  if(value&&typeof value==="object")return `{${Object.keys(value).sort().map(key=>`${JSON.stringify(key)}:${canonicalize(value[key])}`).join(",")}}`;
  return JSON.stringify(value);
};
if(process.argv.includes("--self-test")){
  const {publicKey,privateKey}=generateKeyPairSync("ed25519");
  const record={snapshotId:"contract-1234567890abcdef1234",contentRootSha256:"a".repeat(64),sourceCommit:"b".repeat(40),reviewPacketHash:"c".repeat(64),reviewRole:"product",approverId:"self-test",activeRole:"product-lead",reviewEvidenceHash:"d".repeat(64),decision:"approved",decidedAt:"2026-07-14T00:00:00.000Z"};
  const signature=sign(null,Buffer.from(canonicalize(record)),privateKey);
  if(!verify(null,Buffer.from(canonicalize(record)),publicKey,signature))throw new Error("Approval signer self-test rejected a valid signature");
  if(verify(null,Buffer.from(canonicalize({...record,decision:"rejected"})),publicKey,signature))throw new Error("Approval signer self-test accepted a tampered decision");
  console.log("stage-1 approval signer self-test passed: valid=accepted tampered-decision=rejected");
  process.exit(0);
}
const args={};
for(let index=2;index<process.argv.length;index+=1){
  const token=process.argv[index];
  if(token==="--replace"){args.replace=true;continue;}
  if(!token.startsWith("--")||index+1>=process.argv.length)throw new Error(`Invalid argument: ${token}`);
  args[token.slice(2)]=process.argv[++index];
}
for(const name of ["role","approver-id","active-role","key-id","evidence","private-key","decision"]){
  if(!args[name])throw new Error(`Missing --${name}`);
}
if(!roles.includes(args.role))throw new Error(`--role must be one of ${roles.join(", ")}`);
if(!["approved","rejected"].includes(args.decision))throw new Error("--decision must be approved or rejected");
const privateKeyPath=resolve(args["private-key"]);
const privateKeyRelative=relative(root,privateKeyPath);
if(privateKeyRelative===""||(!privateKeyRelative.startsWith("..")&&privateKeyRelative!==".."))throw new Error("Private keys must be stored outside the repository");
const evidencePath=resolve(args.evidence);
const load=async path=>JSON.parse(await readFile(resolve(root,path),"utf8"));
const packet=await load("gate-reports/stage-1/review-packet.json");
const keyring=await load("gate-reports/stage-1/approver-keyring.json");
const key=keyring.keys.find(item=>item.keyId===args["key-id"]);
if(!key||key.status!=="active")throw new Error("The selected key is not active in the accountable approver keyring");
if(key.approverId!==args["approver-id"]||!key.reviewRoles.includes(args.role))throw new Error("The keyring does not authorize this approver identity for the selected role");
const base={
  snapshotId:packet.snapshotId,contentRootSha256:packet.contentRootSha256,sourceCommit:packet.sourceCommit,reviewPacketHash:packet.packetHash,
  reviewRole:args.role,approverId:args["approver-id"],activeRole:args["active-role"],reviewEvidenceHash:sha256(await readFile(evidencePath)),
  decision:args.decision,decidedAt:args["decided-at"]??new Date().toISOString()
};
if(Number.isNaN(Date.parse(base.decidedAt)))throw new Error("--decided-at must be an ISO 8601 timestamp");
const privateKey=await readFile(privateKeyPath,"utf8");
const signatureValue=sign(null,Buffer.from(canonicalize(base)),privateKey).toString("base64");
if(!verify(null,Buffer.from(canonicalize(base)),key.publicKeyPem,Buffer.from(signatureValue,"base64")))throw new Error("The private key does not match the registered public key");
const record={...base,signature:{kind:"ed25519",keyId:key.keyId,value:signatureValue}};
const output=resolve(root,`gate-reports/stage-1/approvals/${args.role}.json`);
await mkdir(resolve(root,"gate-reports/stage-1/approvals"),{recursive:true});
if(!args.replace){try{await access(output);throw new Error(`Approval already exists at ${output}; inspect it and use --replace explicitly if replacement is intended`);}catch(error){if(error.code!=="ENOENT")throw error;}}
await writeFile(output,`${JSON.stringify(record,null,2)}\n`,args.replace?undefined:{flag:"wx"});
console.log(`stage-1 ${args.role} decision recorded: decision=${record.decision} approver=${record.approverId} evidence=${record.reviewEvidenceHash}`);
