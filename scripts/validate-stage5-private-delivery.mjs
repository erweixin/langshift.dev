import { createHash } from "node:crypto";
import { mkdir, readFile, stat, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const readText = async (path) => readFile(resolve(root, path), "utf8");
const readJSON = async (path) => JSON.parse(await readText(path));
const hash = (value) => createHash("sha256").update(value).digest("hex");
const canonicalHash = (value) => hash(JSON.stringify(value));
const manifest = await readJSON("deploy/private-delivery/manifest.json");
const values = await readText("deploy/helm/lites/values.yaml");
const schema = await readText("deploy/helm/lites/values.schema.json");
const workflow = await readText(".github/workflows/supply-chain.yml");
const install = await readText("docs/private-delivery-installation.md");
const migrationManifest = await readJSON("deploy/migrations/manifest.json");
const legalConfig = await readJSON("config/legal-governance.json");
const results = [];
const check = (id, passed, details) => results.push({ id, status: passed ? "passed" : "failed", details });

const workloadBlock = values.match(/^workloads:\n([\s\S]+)$/m)?.[1] ?? "";
const helmWorkloads = [...workloadBlock.matchAll(/^  ([a-z][a-z0-9-]+):\n    enabled: true$/gm)].map((match) => match[1]);
const sorted = (values) => [...values].sort();
const unique = (values) => new Set(values).size === values.length;
const requiredImages = [...manifest.applicationWorkloads, "lites-migrate", "runtime-host-agent"];

check("MANIFEST-VERSION", manifest.schemaVersion === "1.0.0" && manifest.edition === "Lites Enterprise Private", "private delivery has an explicit versioned edition contract");
check("WORKLOAD-CATALOG", unique(manifest.applicationWorkloads) && JSON.stringify(sorted(manifest.applicationWorkloads)) === JSON.stringify(sorted(helmWorkloads)), "delivery workload catalog exactly matches every enabled Helm workload");
check("IMAGE-CATALOG", unique(manifest.releaseImages) && JSON.stringify(sorted(manifest.releaseImages)) === JSON.stringify(sorted(requiredImages)), "release images cover all workloads plus the migration and Linux runtime-host identities");
check("SUPPLY-CHAIN-COVERAGE", manifest.releaseImages.every((service) => workflow.includes(service)) && workflow.includes("cosign sign") && workflow.includes("attest"), "every delivered image is built by the signing and provenance workflow");
check("DIGEST-INJECTION", schema.includes("sha256:[0-9a-f]{64}") && !values.includes(":latest"), "Helm accepts digest locks and contains no latest tag");
check("PRODUCTION-REPLICAS", [...values.matchAll(/^    replicas: (\d+)$/gm)].every((match) => Number(match[1]) >= 3), "every in-cluster production workload starts with at least three replicas");
check("SECURITY-EVIDENCE", ["cosign signature", "CycloneDX SBOM", "SLSA provenance", "Trivy HIGH/CRITICAL result"].every((item) => manifest.releaseEvidence.requiredPerImage.includes(item)), "the package requires signature, SBOM, provenance and blocking vulnerability evidence per image");
const officialWorkflowIdentity = "https://github.com/erweixin/langshift.dev/.github/workflows/supply-chain.yml@refs/heads/main";
const officialIssuer = "https://token.actions.githubusercontent.com";
check("EVIDENCE-AUTHORITY", manifest.releaseEvidence.cosignCertificateIdentity === officialWorkflowIdentity && manifest.releaseEvidence.cosignCertificateOIDCIssuer === officialIssuer && manifest.stage6EvidenceAuthorities.automatedReports.length === 1 && manifest.stage6EvidenceAuthorities.automatedReports[0].certificateIdentity === officialWorkflowIdentity && manifest.stage6EvidenceAuthorities.automatedReports[0].certificateOIDCIssuer === officialIssuer && ["externalPenetration", "pilotReports", "finalApproval"].every((key) => Array.isArray(manifest.stage6EvidenceAuthorities[key]) && manifest.stage6EvidenceAuthorities[key].length === 0) && install.includes("fail closed"), "official automation identity is immutable and unassigned external authorities fail closed until a new source-bound RC");
check("NO-PLAINTEXT-SECRETS", manifest.security.secrets.includes("no plaintext secret") && values.includes("externalSecretRemoteKey") && install.includes("Never copy plaintext application secrets"), "customer secrets stay in the customer secret manager and outside the delivery bundle");
check("DEFAULT-DENY-MTLS", manifest.security.network.includes("default-deny") && manifest.security.transport.includes("mTLS") && await readText("deploy/helm/lites/templates/networkpolicies.yaml").then((text) => text.includes("policyTypes") && text.includes("Egress")), "delivery requires default-deny networking and workload mTLS");
check("MACOS-EXEMPTION", manifest.supportedPlatform.developmentHost.includes("Firecracker installation and validation are skipped") && install.includes("intentionally not run on macOS") && (await readText("docs/development-macos.md")).includes("Firecracker"), "macOS development skips Firecracker while delivered sandbox hosts remain Linux-only");
check("LIFECYCLE-RUNBOOK", ["Fresh installation", "Upgrade", "Backup and restore", "Rollback"].every((heading) => install.includes(`## ${heading}`)) && manifest.drillPolicy.requiredConsecutivePasses === 3 && manifest.drillPolicy.operations.length === 5, "install, upgrade, backup, restore and rollback are documented with three-run GA drills");
check("MIGRATION-CLOSURE", migrationManifest.migrations.at(-1)?.version === 87 && install.includes("900xxx_verify_current.sql") && install.includes("migration lock"), "delivery binds the current migration chain and post-migration verifier");
check("COMMERCIAL-ENTITLEMENTS", JSON.stringify(manifest.commercialEntitlements) === JSON.stringify(["private_delivery", "commercial_license", "support_tier"]), "private delivery, commercial license and support tier are explicit contract entitlements");
check("TELEMETRY-OPT-IN", manifest.security.telemetry.includes("disabled by default") && manifest.security.telemetry.includes("customer-owned endpoint"), "private delivery does not silently send vendor telemetry");
const legalFiles = await Promise.all(["CLA.md", "CONTRIBUTING.md", "TRADEMARKS.md", "SECURITY.md", "NOTICE", "apps/web/src/components/legal-notice.tsx"].map(readText));
check("LEGAL-GOVERNANCE", legalConfig.policyVersion === "1.0.0" && ["identity_required", "active"].includes(legalConfig.status) && legalConfig.claAcceptanceMethod === "signed_document" && legalFiles.every((text) => text.length > 0) && legalFiles.at(-1).includes("github.com/erweixin/langshift.dev") && (legalConfig.status === "active" || [legalConfig.rightsHolderLegalName, legalConfig.rightsHolderAddress, legalConfig.governingLaw, legalConfig.claSubmissionAddress].every((value) => value === null)), "legal policies and in-product source notice exist; an unassigned rights holder remains explicit and fail-closed for GA");

const artifactHashes = {};
let artifactsPresent = true;
for (const path of manifest.requiredSourceArtifacts) {
  try {
    const info = await stat(resolve(root, path));
    if (!info.isFile()) artifactsPresent = false;
    else artifactHashes[path] = hash(await readText(path));
  } catch {
    artifactsPresent = false;
  }
}
check("SOURCE-ARTIFACTS", artifactsPresent && Object.keys(artifactHashes).length === manifest.requiredSourceArtifacts.length, "every declared source, chart, migration, contract and recovery artifact exists and is hashed");

const failures = results.filter((result) => result.status === "failed");
const base = { reportVersion: "1.0.0", stage: 5, kind: "private-delivery-definition", generatedAt: new Date().toISOString(), status: failures.length ? "failed" : "passed", summary: { checks: results.length, passed: results.length - failures.length, failed: failures.length }, manifestHash: canonicalHash(manifest), migrationManifestHash: canonicalHash(migrationManifest), artifactHashes, results };
const report = { ...base, reportHash: canonicalHash(base) };
const target = resolve(root, "gate-reports/stage-5/private-delivery-definition.json");
await mkdir(dirname(target), { recursive: true });
await writeFile(target, `${JSON.stringify(report, null, 2)}\n`);
console.log(`${report.status}: ${report.summary.passed}/${report.summary.checks} checks; report ${report.reportHash}`);
if (failures.length) {
  for (const failure of failures) console.error(`${failure.id}: ${failure.details}`);
  process.exitCode = 1;
}
