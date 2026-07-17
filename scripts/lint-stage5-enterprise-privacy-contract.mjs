import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";

const root=resolve(import.meta.dirname,"..");
const read=async path=>JSON.parse(await readFile(resolve(root,path),"utf8"));
const hash=value=>createHash("sha256").update(JSON.stringify(value)).digest("hex");
const contract=await read("contracts/openapi/amendments/v1.22.0/enterprise-privacy.json");
const privacy=await read("contracts/catalog/privacy-contract.json");
const enterpriseEvents=await read("contracts/events/amendments/v1.17.0/registry.json");
const enterpriseFixtures=await read("contracts/events/amendments/v1.17.0/upcaster-fixtures.json");
const results=[];
const check=(id,passed,details)=>results.push({id,status:passed?"passed":"failed",details});

check("VERSION-CHAIN",contract.amendmentVersion==="1.22.0"&&contract.baseContractVersion==="1.21.0"&&contract.compatibility==="versioned-client-additive","enterprise privacy contract extends the latest frozen product contract");
const enterpriseOperations=["admin.programs.create.v2","admin.programs.update.v2","admin.cohorts.create.v2","admin.cohorts.enroll.v2","admin.cohorts.unenroll.v2","admin.role-packs.publish.v2"];
check("OPERATIONS",["share-grants.create.v2","share-grants.revoke.v2","admin.aggregate-queries.query.v2",...enterpriseOperations].every(id=>contract.operations[id]),"privacy and enterprise administration operations are versioned explicitly");
check("VENDOR-MEDIA",Object.values(contract.operations).every(operation=>operation.requestContentType?.includes(".v2+json")&&operation.responseContentType?.includes(".v2+json")),"all mutations use strict v2 vendor media types");
check("CLOSED-SCHEMAS",Object.values(contract.schemas).every(schema=>schema.type==="object"&&schema.additionalProperties===false&&schema.required?.length&&schema.properties),"every request and response schema is closed");

const share=contract.schemas.ShareGrantCreateRequestV2;
check("SHARE-EXACT-REVISION",share.required.includes("resource_revision")&&share.properties.resource_revision.minLength===1&&share.properties.resource_revision.maxLength===500,"share grants bind a non-empty bounded exact revision");
check("SHARE-MINIMUM-SCOPE",share.properties.scope.uniqueItems===true&&share.properties.scope.maxItems===2&&JSON.stringify(share.properties.scope.items.enum)===JSON.stringify(["read","review"]),"share scope is unique and limited to read or review");
const revoke=contract.operations["share-grants.revoke.v2"];
check("SHARE-IMMEDIATE-REVOCATION",revoke.concurrency.includes("If-Match")&&revoke.safety.includes("immediately"),"revoke is CAS-bound and denies new authorization immediately after commit");

const request=contract.schemas.AggregateQueryRequestV2;
const response=contract.schemas.AggregateQueryResourceV2;
const frozen=privacy.enterpriseAggregation;
check("AGGREGATE-METRICS",JSON.stringify(request.properties.metric_key.enum)===JSON.stringify(frozen.metrics),"metric catalog exactly matches the frozen privacy catalog");
check("AGGREGATE-DIMENSIONS",request.properties.dimensions.minProperties===1&&request.properties.dimensions.maxProperties===1&&JSON.stringify(request.properties.dimensions.propertyNames.enum)===JSON.stringify(frozen.dimensions),"queries accept exactly one allowlisted dimension");
check("AGGREGATE-BUCKETS",JSON.stringify(request.properties.time_bucket.enum)===JSON.stringify(frozen.timeBuckets),"time buckets exactly match the coarse frozen catalog");
check("NO-PRIVATE-COUNTS",!["cell_size","complement_size","member_id","email","query_hash","cell_key_hash"].some(field=>field in response.properties),"public responses omit cell counts, identities and internal hashes");
check("SUPPRESSION",response.properties.status.enum.includes("suppressed")&&response.properties.value.type.includes("null")&&response.properties.suppression_reason.enum.includes("minimum_cell_size")&&response.properties.suppression_reason.enum.includes("minimum_complement_size"),"suppressed responses cannot carry a required numeric value and identify only the threshold class");
check("QUERY-BUDGET",contract.operations["admin.aggregate-queries.query.v2"].concurrency.includes("atomically")&&contract.operations["admin.aggregate-queries.query.v2"].safety.includes(String(frozen.queryBudget.maxQueriesPerSnapshotPerTenant)),"query budget is fixed and atomically consumed");

check("ENTERPRISE-AUTHZ",enterpriseOperations.every(id=>contract.operations[id].authorization==="active_enterprise_owner_admin_or_program_manager"&&contract.operations[id].csrf==="required"&&contract.operations[id].idempotency==="required_encrypted_durable_response"),"all enterprise mutations require active privileged enterprise membership, CSRF and durable encrypted idempotency");
check("ENTERPRISE-CAS",["admin.programs.update.v2","admin.cohorts.enroll.v2","admin.cohorts.unenroll.v2"].every(id=>contract.operations[id].concurrency.includes("If-Match")),"program and cohort mutable boundaries expose exact If-Match fences");
check("ENROLLMENT-IRREVERSIBLE",contract.operations["admin.cohorts.enroll.v2"].safety.includes("cannot be overwritten or reactivated")&&contract.operations["admin.cohorts.unenroll.v2"].safety.includes("irreversible"),"left enrollment history cannot be deleted or reactivated");
const enroll=contract.schemas.CohortEnrollRequestV2;
const rolePack=contract.schemas.RolePackPublishRequestV2;
check("ENTERPRISE-BOUNDS",enroll.properties.user_ids.maxItems===1000&&enroll.properties.user_ids.uniqueItems===true&&rolePack.properties.role_profile_ids.maxItems===100&&rolePack.properties.task_template_ids.maxItems===500,"batch enrollment and immutable content references have explicit bounded unique sets");
const programUpdated=enterpriseEvents.schemas.ProgramUpdated;
const programFixture=enterpriseFixtures.fixtures.find(fixture=>fixture.eventType==="ProgramUpdated");
check("ENTERPRISE-EVENT-VERSION",enterpriseEvents.amendmentVersion==="1.17.0"&&enterpriseEvents.baseContractVersion==="1.16.0"&&enterpriseEvents.compatibility==="additive"&&programUpdated?.["x-event-type"]==="ProgramUpdated","ProgramUpdated additively extends the latest event amendment");
check("ENTERPRISE-EVENT-CLOSED",programUpdated?.additionalProperties===false&&programUpdated?.properties?.payload?.additionalProperties===false&&programUpdated?.properties?.aggregate_version?.minimum===2,"ProgramUpdated envelope and payload are closed and start after creation version");
check("ENTERPRISE-EVENT-FIXTURE",programFixture?.fromVersion===1&&programFixture?.toVersion===1&&programFixture.inputHash===hash(programFixture.input)&&programFixture.expectedHash===hash(programFixture.expected),"ProgramUpdated canonical fixture is deterministic and hash-bound");

const failures=results.filter(result=>result.status==="failed");
const base={reportVersion:"1.0.0",stage:5,kind:"enterprise-privacy-contract",generatedAt:new Date().toISOString(),status:failures.length?"failed":"passed",summary:{checks:results.length,passed:results.length-failures.length,failed:failures.length},contractHash:hash(contract),results};
const report={...base,reportHash:hash(base)};
const target=resolve(root,"gate-reports/stage-5/enterprise-privacy-contract.json");
await mkdir(dirname(target),{recursive:true});
await writeFile(target,`${JSON.stringify(report,null,2)}\n`);
console.log(`${report.status}: ${report.summary.passed}/${report.summary.checks} checks; report ${report.reportHash}`);
if(failures.length){for(const failure of failures) console.error(`${failure.id}: ${failure.details}`);process.exitCode=1;}
