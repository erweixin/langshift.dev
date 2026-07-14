import { createHash } from "node:crypto";
import { readdir, readFile, stat, writeFile, mkdir } from "node:fs/promises";
import { dirname, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const sha256 = (value) => createHash("sha256").update(typeof value === "string" || Buffer.isBuffer(value) ? value : JSON.stringify(value)).digest("hex");
const load = async (path) => JSON.parse(await readFile(resolve(root,path),"utf8"));
const results=[];
const check=(id,condition,details)=>results.push({id,status:condition?"passed":"failed",details});
const unique=(items)=>new Set(items).size===items.length;

const openapi=await load("contracts/openapi/lites.openapi.json");
const operations=[];
for (const [path,item] of Object.entries(openapi.paths)) for (const [method,op] of Object.entries(item)) operations.push({path,method,op});
check("OPENAPI-VERSION",openapi.openapi==="3.1.0",`version=${openapi.openapi}`);
check("OPENAPI-OPERATION-ID",unique(operations.map(({op})=>op.operationId)),`${operations.length} unique operations`);
check("OPENAPI-METADATA",operations.every(({op})=>["x-authentication","x-authorization","x-idempotency","x-concurrency","x-audit"].every(key=>typeof op[key]==="string")),"all operations declare security, idempotency, concurrency and audit behavior");
check("OPENAPI-WRITES",operations.filter(({method})=>method!=="get").every(({op})=>op.parameters.some(p=>p.$ref==="#/components/parameters/IdempotencyKey")&&op.requestBody&&op.responses["409"]),"all writes declare idempotency, request body and conflict response");
check("OPENAPI-VERSIONED-WRITES",operations.filter(({op})=>op["x-concurrency"].startsWith("If-Match")).every(({op})=>op.parameters.some(p=>p.$ref==="#/components/parameters/IfMatch")),"all versioned writes require If-Match");
const writeOperations=operations.filter(({method})=>method!=="get");
const writeSchemaRefs=writeOperations.map(({op})=>op.requestBody.content["application/json"].schema.$ref);
check("OPENAPI-OPERATION-SCHEMAS",unique(writeSchemaRefs)&&writeSchemaRefs.every(ref=>!ref.endsWith("/WriteRequest")),`${writeSchemaRefs.length} writes use distinct request schemas`);
check("OPENAPI-CLOSED-WRITES",writeSchemaRefs.every(ref=>{const name=ref.split("/").at(-1);return openapi.components.schemas[name]?.additionalProperties===false}),"all write request schemas reject undeclared fields");
check("OPENAPI-DOMAIN-FIELDS",writeSchemaRefs.every(ref=>{const name=ref.split("/").at(-1);return Object.keys(openapi.components.schemas[name]?.properties??{}).length>=2}),"every write schema declares operation-specific domain fields beyond request_id");
check("OPENAPI-PROBLEM",operations.every(({op})=>["400","401","403","409","429"].every(status=>op.responses[status]?.content?.["application/problem+json"])),"all operations use application/problem+json");
check("OPENAPI-SCOPE",!operations.some(({path})=>/oauth|oidc|saml|scim|checkout|payment|stripe/i.test(path)),"excluded identity and online-payment APIs are absent");
check("OPENAPI-PATH-PARAMETERS",operations.every(({path,op})=>[...path.matchAll(/\{([^}]+)\}/g)].every(match=>op.parameters.some(parameter=>parameter.in==="path"&&parameter.name===match[1]&&parameter.required===true))),"every templated path segment has a required path parameter");
check("OPENAPI-SERVICE-IDENTITY",operations.filter(({path})=>path.startsWith("/v1/internal/")).every(({op})=>op["x-authentication"]==="service_identity"&&op.security?.[0]?.serviceMtls&&!("sessionCookie" in op.security[0])),"internal APIs require workload mTLS and never browser sessions");
check("OPENAPI-CURSOR-PAGINATION",operations.filter(({op})=>op.operationId.endsWith(".list")&&op.operationId!=="events.list").every(({op})=>op.parameters.some(parameter=>parameter.$ref==="#/components/parameters/Cursor")&&Object.values(op.responses["200"].content["application/json"].schema)[0].includes("Response")),"resource list operations declare opaque cursor input and collection response schemas");
const eventsListOperation=operations.find(({op})=>op.operationId==="events.list")?.op;
check("OPENAPI-EVENT-CURSOR",eventsListOperation?.parameters.some(parameter=>parameter.$ref==="#/components/parameters/AfterSeq")&&openapi.components.schemas.EventsListResponse?.properties?.next_after_seq,"EventStore backfill uses tenant-user after_seq rather than resource pagination");
check("OPENAPI-ASYNC-RUNS",["onboarding.route_preview","routes.generate","tasks.generate","reviews.generate","projects.test","portfolio.export","messages.create"].every(operationId=>{const op=operations.find(item=>item.op.operationId===operationId)?.op;const ref=Object.values(op?.responses?.["202"]?.content?.["application/json"]?.schema??{})[0];const schema=ref&&openapi.components.schemas[ref.split("/").at(-1)];return schema?.properties?.run_id&&schema?.properties?.status}),"every Agent-starting operation returns 202 with run_id and accepted status");
const realtimeOperation=operations.find(({op})=>op.operationId==="realtime.connect")?.op;
check("OPENAPI-REALTIME-SSE",realtimeOperation?.responses?.["200"]?.content?.["text/event-stream"]&&!realtimeOperation?.responses?.["200"]?.content?.["application/json"],"realtime endpoint is SSE and not a JSON fact source");
check("OPENAPI-SECRET-RESPONSES",Object.entries(openapi.components.schemas).filter(([name])=>/Byok/i.test(name)&&/Response$/.test(name)).every(([,schema])=>!["api_key","secret","secret_ref"].some(field=>field in (schema.properties??{}))),"BYOK responses never expose raw or stored secret material");

const refs=[];
const collectRefs=(value)=>{ if(Array.isArray(value)) value.forEach(collectRefs); else if(value&&typeof value==="object") for(const [key,item] of Object.entries(value)){ if(key==="$ref") refs.push(item); else collectRefs(item); } };
collectRefs(openapi);
const resolveLocalRef=(ref)=>{ if(!ref.startsWith("#/")) return true; let current=openapi; for(const token of ref.slice(2).split("/")) current=current?.[token.replaceAll("~1","/").replaceAll("~0","~")]; return current!==undefined; };
check("OPENAPI-REFS",refs.every(resolveLocalRef),`${refs.length} local references resolved`);

const states=await load("contracts/state-machines/state-machines.json");
for(const [name,machine] of Object.entries(states.machines)){
  const stateSet=new Set(machine.states);
  const initial=Array.isArray(machine.initial)?machine.initial:[machine.initial];
  const targets=Object.values(machine.transitions??{}).flat();
  check(`STATE-${name.toUpperCase()}-REFERENCES`,initial.every(s=>stateSet.has(s))&&targets.every(s=>stateSet.has(s))&&Object.keys(machine.transitions??{}).every(s=>stateSet.has(s)),`${machine.states.length} states and ${targets.length} transitions`);
  check(`STATE-${name.toUpperCase()}-TERMINAL`,(machine.terminal??[]).every(s=>(machine.transitions?.[s]??[]).length===0),"terminal states have no ordinary outgoing transitions");
}
check("STATE-ANON-CLAIM-PATH",JSON.stringify(states.machines.anonymous_claim.transitions.available)==='["reserved","expired"]'&&states.machines.anonymous_claim.transitions.erasing.includes("claimed"),"claim follows reserve, destination commit, erase and claimed boundaries");
check("STATE-ROUTE-CAS",states.machines.route_revision.cas.includes("claim_set_hash"),"route result is guarded by claim_set_hash");
check("STATE-PROJECT-GUARDS",states.machines.project.completionGuards.length>=5,"project completion requires milestones, validation, reflection and revision manifest");

const events=await load("contracts/events/registry.json");
const eventNames=Object.keys(events.schemas);
check("EVENT-UNIQUE",unique(eventNames),`${eventNames.length} unique event types`);
check("EVENT-IDS",unique(Object.values(events.schemas).map(schema=>schema.$id)),"event schema identifiers are unique");
check("EVENT-METADATA",Object.values(events.schemas).every(schema=>schema["x-event-schema-version"]===1&&schema["x-owner"]&&schema.required.includes("tenant_id")&&schema.required.includes("user_id")),"every event is versioned, owned and principal-scoped");
check("EVENT-CLOSED-PAYLOADS",Object.values(events.schemas).every(schema=>schema.properties.payload.additionalProperties===false&&schema.properties.payload.required.length>=2),"every event has a closed, event-specific payload contract");
const upcasters=await load("contracts/events/upcaster-fixtures.json");
const fixtureValueMatches=(value,field)=>{
  if(field.const!==undefined&&value!==field.const)return false;
  if(field.enum&&!field.enum.includes(value))return false;
  if(field.type==="integer"&&(!Number.isInteger(value)||value<(field.minimum??-Infinity)))return false;
  if(field.type==="number"&&(typeof value!=="number"||value<(field.minimum??-Infinity)))return false;
  if(field.type==="array"&&(!Array.isArray(value)||value.length<(field.minItems??0)))return false;
  if(field.format==="date-time"&&Number.isNaN(Date.parse(value)))return false;
  if(field.format==="date"&&!/^\d{4}-\d{2}-\d{2}$/.test(value))return false;
  return true;
};
check("EVENT-UPCASTER-FIXTURES",upcasters.fixtures.length===eventNames.length&&unique(upcasters.fixtures.map(fixture=>fixture.fixtureId))&&upcasters.fixtures.every(fixture=>{const schema=events.schemas[fixture.eventType]?.properties?.payload;return fixture.inputHash===fixture.expectedHash&&fixture.fromVersion===1&&fixture.toVersion===1&&schema?.required.every(field=>field in fixture.input&&fixtureValueMatches(fixture.input[field],schema.properties[field]));}),`${upcasters.fixtures.length} deterministic schema-valid canonical fixtures`);
const criticalEventRequirements={AnonymousClaimDestinationCommitted:["claim_key","target_tenant_id","target_user_id","mission_id","route_revision_id","commit_event_id"],MissionFocusChanged:["previous_mission_id","mission_id","previous_focus_version","focus_version"],RouteProposed:["base_route_version","claim_set_hash","input_manifest_hash","profile_snapshot_id","ontology_snapshot_id","content_snapshot_id"],RouteMarkedStale:["expected_claim_set_hash","current_claim_set_hash","reason_code"],RunStarted:["command_id","attempt_id","fence","lease_token_hash","lease_expires_at"],ToolCallOutcomeUnknown:["effect_key","request_hash","reconciliation_due_at"],WorkspaceRevisionCommitAuthorized:["base_revision","prepared_revision","prepared_hash","authorization_id","effect_key","commit_command_id"],ApprovalGranted:["approver_user_id","proposal_hash","target_version","permission_snapshot","reauthenticated_at"],ProviderAttemptRecorded:["provider_attempt_id","model_id","input_tokens","output_tokens","cost_microunits","context_manifest_hash"],UsageSettled:["reservation_id","operation_key","actual_units","ledger_entry_id","provider_attempt_id"],RepairCommandApproved:["proposal_hash","target_version","approval_event_ids"]};
check("EVENT-CRITICAL-INVARIANTS",Object.entries(criticalEventRequirements).every(([eventType,fields])=>fields.every(field=>events.schemas[eventType]?.properties?.payload?.required?.includes(field))),"claim, focus, route, worker lease, effect, workspace, approval, provider, usage and repair events carry recovery-critical fields");

const database=await load("contracts/database/schema-catalog.json");
check("DATABASE-UNIQUE",unique(database.tables.map(table=>table.qualifiedName)),`${database.tables.length} unique table contracts`);
check("DATABASE-RLS",database.tables.every(table=>table.rls),"every table declares its service or tenant access boundary");
check("DATABASE-DATA-CLASS",database.tables.every(table=>["public","internal","confidential","restricted","secret"].includes(table.dataClass)),"every table has a data classification");
const duplicateColumns=database.tables.flatMap(table=>{const names=table.columns.map(column=>column.trim().split(/\s+/)[0].replaceAll(/[\"(),]/g,""));return [...new Set(names.filter((name,index)=>names.indexOf(name)!==index))].map(name=>`${table.qualifiedName}.${name}`)});
check("DATABASE-COLUMNS-UNIQUE",duplicateColumns.length===0,duplicateColumns.length?duplicateColumns.join(", "):"column names are unique within every table");
check("DATABASE-NO-GENERIC-PAYLOAD",database.tables.every(table=>!table.columns.some(column=>/^payload\s/i.test(column))),"domain tables do not hide their model behind a generic payload column");
check("DATABASE-LOCK-ORDER",database.lockOrder.at(-1)==="append_rows"&&database.lockOrder.indexOf("event_cursor")>database.lockOrder.indexOf("parent_aggregate"),"event cursor is locked after business and coordination rows");
const ddl=await readFile(resolve(root,"contracts/database/000001_contract_baseline.sql"),"utf8");
check("DATABASE-DDL-TABLES",database.tables.every(table=>ddl.includes(`CREATE TABLE IF NOT EXISTS \"${table.schema}\".\"${table.name}\"`)),"every catalog table is represented in generated DDL");
check("DATABASE-DDL-RLS",database.tables.filter(table=>table.tenantScoped).every(table=>ddl.includes(`CREATE POLICY \"${table.name}_tenant_isolation\"`)),"every tenant-scoped table has a generated forced RLS policy");
const databaseVerification=await readFile(resolve(root,"contracts/database/900000_verify_contract.sql"),"utf8");
check("DATABASE-EXECUTABLE-VERIFICATION",["expected 90 contract tables","expected 83 forced-RLS tables","expected 21 append-only triggers","cross-tenant row became visible","append-only mutation unexpectedly succeeded","claim_key uniqueness missing","session active tenant binding missing","session CAS version missing","session active tenant foreign key missing","idempotency response scope uniqueness missing","Mission Focus primary key missing","effect ledger uniqueness missing","two-person distinct-vote constraint missing","credit conservation check missing"].every(marker=>databaseVerification.includes(marker)),"database verification covers catalog, isolation, immutability and critical domain constraints");
const sessionTable=database.tables.find(table=>table.qualifiedName==="identity.sessions");
check("DATABASE-SESSION-TENANT-CONTEXT",sessionTable.columns.some(column=>column.startsWith("active_tenant_id uuid NOT NULL"))&&sessionTable.columns.some(column=>column.startsWith("version bigint"))&&sessionTable.foreignKeys.some(([column,target])=>column==="active_tenant_id"&&target==="identity.tenants"),"sessions persist a CAS-versioned active tenant that must resolve through an active membership");
const idempotencyTable=database.tables.find(table=>table.qualifiedName==="agent.idempotency_responses");
check("DATABASE-IDEMPOTENCY-RESPONSES",idempotencyTable?.tenantScoped&&idempotencyTable?.userScoped&&idempotencyTable?.dataClass==="restricted"&&idempotencyTable.uniques.some(columns=>JSON.stringify(columns)===JSON.stringify(["tenant_id","user_id","operation_id","idempotency_key_hash"]))&&["request_hash","response_payload_ref","response_hash","expires_at"].every(field=>idempotencyTable.columns.some(column=>column.startsWith(`${field} `))),"idempotency responses are tenant-user-operation scoped, content-addressed, expiring and never store raw keys or response bodies");
const databaseSmoke=await load("gate-reports/stage-1/database-smoke.json");
check("DATABASE-SMOKE-EVIDENCE",databaseSmoke.status==="passed"&&databaseSmoke.source.ddlSha256===sha256(ddl)&&databaseSmoke.source.verificationSha256===sha256(databaseVerification)&&databaseSmoke.results.crossTenantVisibleRows===0&&databaseSmoke.results.appendOnlyMutationsSucceeded===0,"database smoke evidence hashes match current DDL and zero-tolerance assertions passed");

const errors=await load("contracts/catalog/error-codes.json");
check("ERROR-CODES",unique(errors.errors.map(error=>error.code))&&errors.errors.every(error=>Number.isInteger(error.status)&&typeof error.retryable==="boolean"),`${errors.errors.length} unique typed errors`);
const errorByCode=new Map(errors.errors.map(error=>[error.code,error]));
check("ERROR-OPENAPI-ENUM",JSON.stringify(openapi.components.schemas.Problem.properties.code.enum)===JSON.stringify(errors.errors.map(error=>error.code)),"Problem.code enum is generated from the canonical error catalog");
check("ERROR-OPERATION-BINDING",operations.every(({op})=>op["x-error-codes"]?.length&&op["x-error-codes"].every(code=>errorByCode.has(code)&&op.responses[String(errorByCode.get(code).status)])),"every operation error code resolves to the catalog and a matching HTTP response");
check("ERROR-VERSION-PRECONDITION",operations.filter(({op})=>op["x-concurrency"].startsWith("If-Match")).every(({op})=>op["x-error-codes"].includes("version_conflict")&&op["x-error-codes"].includes("precondition_required")&&op.responses["428"]),"versioned writes declare both missing and stale precondition outcomes");
const retention=await load("contracts/catalog/retention-policy.json");
check("RETENTION",retention.rules.length>=10&&retention.rules.every(rule=>rule.record&&rule.class&&rule.retention&&rule.deletion),`${retention.rules.length} classified retention rules`);
const dataClassification=await load("contracts/catalog/data-classification.json");
check("DATA-CLASSIFICATION-LEVELS",JSON.stringify(dataClassification.classes.map(item=>item.id))===JSON.stringify(["public","internal","confidential","restricted","secret"]),"five ordered data classes are defined");
check("DATA-CLASSIFICATION-SECRETS",["password","raw_auth_token","byok_plaintext"].every(id=>{const record=dataClassification.records.find(item=>item.id===id);return record?.class==="secret"&&record.prohibited.includes("log")&&record.prohibited.includes("prompt")&&record.prohibited.includes("artifact")}),"passwords, raw tokens and BYOK plaintext are prohibited from logs, prompts and artifacts");
check("DATA-CLASSIFICATION-PRIVATE",["private_body","memory_document","workspace_and_artifact"].every(id=>dataClassification.records.some(record=>record.id===id&&record.class==="restricted"&&record.deletion)),"private bodies, Memory, Workspace and Artifacts have restricted storage and deletion rules");
check("DATA-PAYLOAD-ENVELOPE",["payload_ref","ciphertext_hash","key_ref","key_version","classification","content_type","plaintext_length","created_at"].every(field=>dataClassification.payloadEnvelope.required.includes(field))&&dataClassification.payloadEnvelope.associatedData.includes("tenant_id")&&dataClassification.payloadEnvelope.associatedData.includes("user_id"),"payload envelope binds classification, key version, integrity and tenant-user associated data");
check("DATA-DEFAULT-RESTRICTED",dataClassification.defaultRule.includes("restricted"),"unknown data fails closed as restricted");
const privacy=await load("contracts/catalog/privacy-contract.json");
check("PRIVACY-K",privacy.enterpriseAggregation.minimumCellSize>=5&&privacy.enterpriseAggregation.minimumComplementSize>=5,"cell and complement k are at least five");
check("PRIVACY-TWO-PERSON",privacy.twoPersonRule.approvalsRequired===2&&!privacy.twoPersonRule.initiatorMayApprove&&privacy.twoPersonRule.distinctActivePrincipals,"two distinct active non-initiator approvals required");
const threatModel=await load("contracts/security/threat-model.json");
const requiredThreatCategories=["cross_tenant_access","secret_exfiltration","approval_bypass","unauthorized_tool","network_egress","path_escape","prompt_injection","malicious_retrieval","memory_poisoning","dangerous_artifact","duplicate_effect","stale_worker","anonymous_claim_race","ledger_corruption","deletion_gap"];
check("THREAT-MODEL-COVERAGE",requiredThreatCategories.every(category=>threatModel.threats.some(threat=>threat.category===category&&threat.controls.length&&threat.tests.length)),`${threatModel.threats.length} platform threats cover every required security category`);
check("THREAT-MODEL-CRITICAL",threatModel.threats.filter(threat=>threat.severity==="critical").every(threat=>threat.residualRisk&&threat.controls.length>=3),"every critical threat has explicit controls and residual-risk disposition");
check("THREAT-MODEL-PRIVACY",threatModel.enterpriseAggregationThreats.length>=7&&threatModel.enterpriseAggregationThreats.every(threat=>threat.control&&threat.test),"enterprise aggregation model covers cell, complement, differencing, adaptive, snapshot, dimension and tenant attacks");
check("THREAT-MODEL-TWO-PERSON",threatModel.twoPersonThreats.length>=5&&threatModel.twoPersonThreats.every(threat=>threat.control&&threat.test),"two-person model covers identity, replay, mutation, role and reauthentication threats");

const requirements=await load("contracts/traceability/requirements.json");
check("TRACE-UNIQUE",unique(requirements.requirements.map(item=>item.requirementId)),`${requirements.requirements.length} unique requirements`);
check("TRACE-COMPLETE",requirements.requirements.every(item=>item.ui.length&&item.api.length&&item.contract.length&&item.data.length&&item.authorization&&item.automatedTestIds.length&&item.gate.length),"every requirement maps to UI, API, contract, data, auth, tests and gate");
const traceOperationIds=new Set(operations.map(({op})=>op.operationId));
const traceDatabaseTables=new Set(database.tables.map(table=>table.qualifiedName));
check("TRACE-UI-ROUTES",requirements.requirements.every(item=>item.ui.every(route=>route.startsWith("/"))),"every requirement references concrete UI routes");
check("TRACE-API-REFERENCES",requirements.requirements.every(item=>item.api.every(operationId=>traceOperationIds.has(operationId))),"every requirement API reference resolves to OpenAPI");
check("TRACE-DATA-REFERENCES",requirements.requirements.every(item=>item.data.every(table=>traceDatabaseTables.has(table))),"every requirement data reference resolves to a database table");
const capabilityInventory=await load("contracts/traceability/capability-inventory.json");
const requirementIds=new Set(requirements.requirements.map(item=>item.requirementId));
check("CAPABILITY-INVENTORY-UNIQUE",unique(capabilityInventory.capabilities.map(item=>item.capabilityId)),`${capabilityInventory.capabilities.length} capability ids are unique`);
check("CAPABILITY-INVENTORY-REQUIREMENTS",capabilityInventory.capabilities.every(item=>requirementIds.has(item.requirementId))&&requirements.requirements.every(requirement=>capabilityInventory.capabilities.some(item=>item.requirementId===requirement.requirementId)),"every capability resolves to a requirement and every requirement is decomposed into capabilities");
check("CAPABILITY-INVENTORY-SECTIONS",["product-capabilities","commercial-model","technical-implementation"].every(section=>capabilityInventory.capabilities.some(item=>item.sourceSection===section)),"implementation-plan product, commercial and technical sections are represented");
check("CAPABILITY-INVENTORY-TRACE",capabilityInventory.capabilities.every(item=>item.ui.length&&item.api.every(operationId=>traceOperationIds.has(operationId))&&item.contracts.length&&item.data.every(table=>traceDatabaseTables.has(table))&&item.authorization&&item.testIds.length&&item.gates.length),"every individual capability maps to real UI, API, contract, data, authorization, tests and gates");
const journeys=await load("contracts/traceability/main-journeys.json");
check("JOURNEY-FIVE",journeys.journeys.length===5,"five required main journeys are frozen");
check("JOURNEY-TRACE",journeys.journeys.every(j=>j.steps.every(s=>s.ui&&s.api?.length&&s.eventOrState?.length&&s.data?.length&&s.testId)),"every journey step maps UI through test");
const operationIds=new Set(operations.map(({op})=>op.operationId));
const eventTypeSet=new Set(eventNames);
const databaseTableSet=new Set(database.tables.map(table=>table.qualifiedName));
check("JOURNEY-API-REFERENCES",journeys.journeys.every(journey=>journey.steps.every(step=>step.api.every(operationId=>operationIds.has(operationId)))),"every journey API reference resolves to an OpenAPI operation");
check("JOURNEY-EVENT-REFERENCES",journeys.journeys.every(journey=>journey.steps.every(step=>step.eventOrState.every(eventType=>eventTypeSet.has(eventType)))),"every journey event reference resolves to the Event Registry");
check("JOURNEY-DATA-REFERENCES",journeys.journeys.every(journey=>journey.steps.every(step=>step.data.every(table=>databaseTableSet.has(table)))),"every journey data reference resolves to the database catalog");

const safety=await load("security-tests/fixed-safety-samples.json");
const categoryCounts=Object.groupBy(safety.samples,sample=>sample.category);
check("SAFETY-COUNT",safety.samples.length>=500,`${safety.samples.length} fixed samples`);
check("SAFETY-CATEGORIES",Object.keys(categoryCounts).length===10&&Object.values(categoryCounts).every(samples=>samples.length>=50),"ten categories each contain at least fifty samples");
check("SAFETY-UNIQUE",unique(safety.samples.map(sample=>sample.id)),"security sample identifiers are unique");
check("SAFETY-EXECUTABLE",unique(safety.samples.map(sample=>sample.input))&&safety.samples.every(sample=>sample.input.length>=40&&sample.assertions?.length>=4&&!sample.input.includes("adversarial fixture variant")),"all security samples contain unique executable attack payloads and explicit assertions");

const productEvals=await load("product-evals/bilingual-transition-evals.json");
const byLocale=Object.groupBy(productEvals.samples,sample=>sample.locale);
check("EVAL-PRODUCT-COUNT",byLocale.en?.length>=300&&byLocale["zh-CN"]?.length>=300,`${byLocale.en?.length} English and ${byLocale["zh-CN"]?.length} Chinese samples`);
check("EVAL-PRODUCT-GROUPS",new Set(productEvals.samples.map(sample=>sample.transition)).size>=30,"at least thirty transition groups");
const productSemanticGroups=Object.groupBy(productEvals.samples,sample=>sample.semanticKey);
check("EVAL-PRODUCT-EXECUTABLE",productEvals.samples.every(sample=>sample.input?.mission&&sample.input?.claims?.length&&sample.input?.evidence?.length&&sample.expected?.transferBridge&&sample.expected?.firstTask&&!sample.inputFixture),"every product eval contains actual mission, claim, evidence, transfer, gap and task expectations");
check("EVAL-PRODUCT-BILINGUAL-PAIRS",Object.values(productSemanticGroups).every(samples=>samples.length===2&&new Set(samples.map(sample=>sample.locale)).size===2),`${Object.keys(productSemanticGroups).length} semantic scenarios have exact English and Chinese pairs`);
const profileContracts=await load("contracts/catalog/profile-contracts.json");
const profileSchemas=await load("contracts/profiles/registry.json");
const profileRubrics=await load("contracts/profiles/rubrics.json");
check("PROFILE-SCHEMA-REFERENCES",profileContracts.profiles.every(profile=>profileSchemas.schemas[profile.inputSchema]?.$id===profile.inputSchema&&profileSchemas.schemas[profile.outputSchema]?.$id===profile.outputSchema),"every Profile input and output schema reference resolves");
check("PROFILE-SCHEMAS-CLOSED",Object.values(profileSchemas.schemas).every(schema=>schema.additionalProperties===false&&schema.required?.length&&schema.properties),`${Object.keys(profileSchemas.schemas).length} Profile schemas are closed and require explicit fields`);
check("PROFILE-RUBRIC-DIMENSIONS",profileContracts.profiles.every(profile=>{const rubric=profileRubrics.profiles[profile.id];return rubric&&JSON.stringify(rubric.dimensions.map(dimension=>dimension.id))===JSON.stringify(profile.rubric)&&rubric.deterministicChecks.length}),"each Profile rubric defines exactly its frozen dimensions and deterministic checks");
check("PROFILE-RUBRIC-ANCHORS",Object.values(profileRubrics.profiles).every(rubric=>rubric.dimensions.every(dimension=>["1","2","3","4","5"].every(score=>dimension.anchors[score]))),"every rubric dimension has shared five-point scoring anchors");
check("PROFILE-RUBRIC-GATES",profileRubrics.scoring.meanPerDimensionMinimum===4&&profileRubrics.scoring.acceptableRateMinimum===0.85&&profileRubrics.scoring.minimumGroupRate===0.80&&profileRubrics.scoring.maxLocaleGapPoints===5&&profileRubrics.scoring.reviewerAgreementMinimum===0.85&&profileRubrics.scoring.reviewersRequired===2,"rubric thresholds match the fixed implementation gate");
const profileRequiredInputKeys={route_planner:["targetRequirements","claimSetHash","baseRouteVersion","ontologySnapshotId","contentSnapshotId"],daily_planner:["acceptedRoute","focus","weeklyMinutes","recentEvidenceIds"],coach:["selectedText","conversationThroughSeq","preference","allowedContextIds","requestedAction"],evaluator:["submission","deterministicTests","rubric"],artifact_builder:["project","workspaceRevision","artifactRevisions","evidenceManifest"]};
const profileRequiredExpectedKeys={route_planner:["claimSetHash","citations","staleIfHashChanges"],daily_planner:["missionId","focusVersion","causalReferences","oneTask"],coach:["mustUseOnlyContextIds","mustNotInventContext","mustNotExecuteRestrictedAction"],evaluator:["deterministicDecision","mayUpgradeCapability","evidenceRequired"],artifact_builder:["completionAllowed","manifestRequired","externalWriteAllowed","exactWorkspaceRevision"]};
for(const profile of profileContracts.profiles){
  const slice=await load(`profile-evals/${profile.id}/bilingual-slice.json`);
  const counts=Object.groupBy(slice.samples,sample=>sample.locale);
  check(`EVAL-${profile.id.toUpperCase()}`,counts.en?.length>=100&&counts["zh-CN"]?.length>=100&&slice.samples.every(sample=>sample.expected.rubric.length===profile.rubric.length),`${counts.en?.length} English and ${counts["zh-CN"]?.length} Chinese profile samples`);
  check(`EVAL-${profile.id.toUpperCase()}-EXECUTABLE`,slice.samples.every(sample=>sample.input&&sample.input.mission&&sample.input.evidence?.length&&!sample.inputFixture&&sample.expected.unauthorizedEffects===0&&profileRequiredInputKeys[profile.id].every(key=>sample.input[key]!==undefined)&&profileRequiredExpectedKeys[profile.id].every(key=>sample.expected[key]!==undefined)),"profile slice contains role-specific context and deterministic boundary expectations");
  const indexedPairs=Object.groupBy(slice.samples,sample=>sample.id.replace("-ZH-CN-","-").replace("-EN-","-"));
  check(`EVAL-${profile.id.toUpperCase()}-BILINGUAL`,Object.values(indexedPairs).every(samples=>samples.length===2&&new Set(samples.map(sample=>sample.locale)).size===2),`${Object.keys(indexedPairs).length} indexed bilingual pairs`);
}

const protocol=await load("pilot-protocols/design-partner-v1.json");
check("PILOT-PROTOCOL",protocol.durationDays>=28&&protocol.minimumConsentedParticipants>=60&&protocol.metrics.length===5&&protocol.zeroTolerance.length>=4,"duration, cohort, metrics, consent and zero-tolerance rules are frozen");
check("PILOT-RECRUITMENT-STRATA",protocol.recruitmentStrata.length>=6&&protocol.recruitmentStrata.every(stratum=>stratum.minimum.en>=5&&stratum.minimum["zh-CN"]>=5)&&protocol.eligibility.include.length&&protocol.eligibility.exclude.length,"six bilingual transition strata and eligibility rules are frozen before recruitment");
const pilotConsent=await load("pilot-protocols/consent-v1.json");
const consentFields=["title","purpose","procedures","data","risks","benefits","voluntary","withdrawal","contact"];
check("PILOT-CONSENT-BILINGUAL",["en","zh-CN"].every(locale=>consentFields.every(field=>pilotConsent.locales[locale]?.[field]))&&pilotConsent.requiredAffirmations.length>=5,"English and Chinese consent disclose purpose, procedure, data, risk, benefit, voluntariness, withdrawal and contact");
check("PILOT-CONSENT-MINIMIZATION",["conversation_body","reflection_body","password","raw_token","byok_secret","employer_private_body"].every(field=>pilotConsent.record.prohibited.includes(field)),"pilot consent record prohibits private bodies and secrets");
const cohortManifestSchema=await load("pilot-protocols/cohort-manifest.schema.json");
check("PILOT-COHORT-SCHEMA",cohortManifestSchema.additionalProperties===false&&cohortManifestSchema.properties.participantCount.minimum===60&&cohortManifestSchema.properties.localeCounts.properties.en.minimum===30&&cohortManifestSchema.properties.localeCounts.properties["zh-CN"].minimum===30&&cohortManifestSchema.properties.stratumCounts.minItems>=6,"cohort evidence enforces total, locale and stratum minimums");
const pilotReportTemplate=await load("pilot-protocols/report-template-v1.json");
check("PILOT-REPORT-METRICS",JSON.stringify(pilotReportTemplate.metricRows.map(metric=>metric.id))===JSON.stringify(protocol.metrics.map(metric=>metric.id))&&pilotReportTemplate.metricRows.every(metric=>protocol.metrics.find(item=>item.id===metric.id)?.threshold===metric.threshold),"report template preserves frozen metric ids, denominators and thresholds");
check("PILOT-REPORT-HONESTY",pilotReportTemplate.status==="template_not_evidence"&&pilotReportTemplate.metricRows.every(metric=>metric.result===null&&metric.passed===null)&&pilotReportTemplate.zeroToleranceRows.every(row=>row.count===null&&row.required===0),"pilot template cannot be mistaken for passing evidence");
const impact=await load("behavior-manifests/impact-matrix.json");
check("BEHAVIOR-IMPACT",new Set(impact.rules.map(rule=>rule.kind)).size===12&&impact.rules.every(rule=>rule.invalidates.length),"all behavior component kinds have invalidation rules");
const contractSnapshot=await load("gate-reports/stage-1/contract-snapshot.json");
const snapshotFilesCurrent=[];
for(const file of contractSnapshot.files){const body=await readFile(resolve(root,file.path));snapshotFilesCurrent.push({path:file.path,sha256:sha256(body)});}
check("CONTRACT-SNAPSHOT-FILES",JSON.stringify(snapshotFilesCurrent)===JSON.stringify(contractSnapshot.files),`${contractSnapshot.files.length} contract assets match their frozen hashes`);
check("CONTRACT-SNAPSHOT-ROOT",sha256(snapshotFilesCurrent)===contractSnapshot.contentRootSha256&&contractSnapshot.snapshotId===`contract-${contractSnapshot.contentRootSha256.slice(0,20)}`,`content root ${contractSnapshot.contentRootSha256}`);
const reviewIssues=await load("gate-reports/stage-1/review-issues.json");
check("CONTRACT-REVIEW-ISSUES",reviewIssues.openP0.length===0&&reviewIssues.openP1.length===0&&reviewIssues.openOther.length===0,"known contract review issues are resolved with evidence; accountable approvals remain separate");
const gateStatus=await load("gate-reports/stage-1/gate-status.json");
check("CONTRACT-GATE-HONESTY",["in_progress","failed","passed"].includes(gateStatus.status)&&Object.values(contractSnapshot.approvals).every(approval=>approval===null),"snapshot never embeds inferred approvals; gate status can advance only through separately verified signed records");
const approvalRecordSchema=await load("contracts/reviews/approval-record.schema.json");
check("CONTRACT-APPROVAL-SCHEMA",approvalRecordSchema.additionalProperties===false&&["snapshotId","contentRootSha256","sourceCommit","reviewPacketHash","reviewRole","approverId","activeRole","reviewEvidenceHash","decision","decidedAt","signature"].every(field=>approvalRecordSchema.required.includes(field))&&approvalRecordSchema.properties.reviewRole.enum.length===5&&approvalRecordSchema.properties.signature.properties.kind.enum.includes("ed25519"),"approval records bind source commit, snapshot, review packet, five accountable roles, evidence, identity, time and an Ed25519 signature");
const reviewChecklist=await load("contracts/reviews/stage-1-review-checklist.json");
check("CONTRACT-APPROVAL-CHECKLIST",JSON.stringify(reviewChecklist.approvalRecordRequired)===JSON.stringify(approvalRecordSchema.required)&&Object.keys(reviewChecklist.roles).length===5,"the accountable review checklist names all five roles and the exact signed approval fields");
const approverKeyringSchema=await load("contracts/reviews/approver-keyring.schema.json");
check("CONTRACT-APPROVER-KEYRING",approverKeyringSchema.additionalProperties===false&&approverKeyringSchema.properties.keys.minItems>=5&&["keyId","approverId","reviewRoles","publicKeyPem","status","registeredAt"].every(field=>approverKeyringSchema.properties.keys.items.required.includes(field)),"approver keyring binds active public keys to identities and review roles");
const gateReportSchema=await load("contracts/reviews/gate-report.schema.json");
check("CONTRACT-GATE-REPORT-SCHEMA",gateReportSchema.additionalProperties===false&&["acceptanceTarget","environment","versions","results","failures","repairCommits","sourceCommit","evidenceCommit","snapshot","approvals","blockers","reportHash"].every(field=>gateReportSchema.required.includes(field))&&gateReportSchema.properties.results.minItems>=5&&gateReportSchema.allOf.some(rule=>rule.then?.properties?.approvals?.minItems===5&&rule.then?.properties?.blockers?.maxItems===0),"stage gate report requires target, environment, immutable versions, results, failures, repairs, commits, snapshot, five approvals, blockers and report hash");
const gateReport=await load("gate-reports/stage-1/gate-report.json");
const gateReportUnsigned=Object.fromEntries(Object.entries(gateReport).filter(([key])=>key!=="reportHash"));
check("CONTRACT-GATE-REPORT-HASH",gateReport.reportHash===sha256(gateReportUnsigned),`gate report ${gateReport.reportHash} is content-addressed`);
check("CONTRACT-GATE-REPORT-EVIDENCE",gateReport.results.length>=5&&(await Promise.all(gateReport.results.map(async item=>item.evidenceSha256===sha256(await readFile(resolve(root,item.evidencePath)))))).every(Boolean),"every gate result is bound to the current evidence file");
check("CONTRACT-GATE-REPORT-VERSIONS",gateReport.versions.configurationSnapshot.snapshotId===contractSnapshot.snapshotId&&gateReport.versions.configurationSnapshot.contentRootSha256===contractSnapshot.contentRootSha256&&gateReport.versions.databaseSchema.ddlSha256===databaseSmoke.source.ddlSha256&&gateReport.versions.testData.length===7&&gateReport.versions.testData.reduce((total,item)=>total+item.sampleCount,0)===2100,"gate report binds the contract root, database schema and all 2,100 fixed evaluation samples");
const gateReportNotPassed=["in_progress","failed"].includes(gateReport.status)&&(gateReport.sourceCommit===null||/^[a-f0-9]{40}$/.test(gateReport.sourceCommit))&&gateReport.approvals.length===0&&gateReport.blockers.length>=1&&gateStatus.status===gateReport.status&&gateStatus.readiness==="technical_contract_ready_for_accountable_review";
const gateReportPassed=gateReport.status==="passed"&&/^[a-f0-9]{40}$/.test(gateReport.sourceCommit??"")&&gateReport.approvals.length===5&&new Set(gateReport.approvals.map(item=>item.role)).size===5&&gateReport.blockers.length===0&&gateStatus.status==="passed"&&gateStatus.readiness==="approved_for_stage_2";
check("CONTRACT-GATE-REPORT-HONESTY",(gateReportNotPassed||gateReportPassed)&&!gateReport.acceptanceTarget.releaseEligible,"pre-review evidence cannot claim approval, and a passed Stage 1 report requires five unique verified roles while remaining non-GA");

const rootsToScan=["contracts","security-tests","product-evals","profile-evals","pilot-protocols","behavior-manifests"];
const files=[];
const walk=async path=>{ for(const entry of await readdir(resolve(root,path))){ const relative=`${path}/${entry}`; const info=await stat(resolve(root,relative)); if(info.isDirectory()) await walk(relative); else files.push(relative); } };
for(const path of rootsToScan) await walk(path);
const forbidden=[];
for(const path of files){ const body=await readFile(resolve(root,path),"utf8"); if(/\b(?:TBD|TODO)\b/.test(body)) forbidden.push(path); }
for(const path of ["README.md","docs/architecture.md","docs/product-implementation-plan.md","docs/ux-prototype.html"]){
  const lines=(await readFile(resolve(root,path),"utf8")).split("\n");
  if(lines.some(line=>/\b(?:TBD|TODO)\b/.test(line)&&!line.includes("文档中不得存在影响实现"))) forbidden.push(path);
}
check("NO-UNRESOLVED-MARKERS",forbidden.length===0,forbidden.length?forbidden.join(", "):`${files.length} assets scanned`);

const failures=results.filter(result=>result.status==="failed");
const reportBase={reportVersion:"1.0.0",stage:1,kind:"schema-lint",generatedAt:new Date().toISOString(),status:failures.length?"failed":"passed",summary:{checks:results.length,passed:results.length-failures.length,failed:failures.length},results};
const report={...reportBase,reportHash:sha256(reportBase)};
const reportPath=resolve(root,"gate-reports/stage-1/schema-lint.json");
await mkdir(dirname(reportPath),{recursive:true});
await writeFile(reportPath,`${JSON.stringify(report,null,2)}\n`);
console.log(`${report.status}: ${report.summary.passed}/${report.summary.checks} checks passed; report ${report.reportHash}`);
if(failures.length){ for(const failure of failures) console.error(`${failure.id}: ${failure.details}`); process.exitCode=1; }
