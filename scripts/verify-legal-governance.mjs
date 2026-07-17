import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

const requiredAttestations = [
  "I have an active signed Lites Individual or Entity CLA covering this GitHub identity and contribution.",
  "I own or am authorized to submit this work and have disclosed all third-party material and employer rights.",
  "This change contains no credential, private key, token, customer data or undisclosed vulnerability detail.",
  "I have read `CONTRIBUTING.md`, `SECURITY.md` and `TRADEMARKS.md`.",
];

export function validateLegalGovernance(config, { requireActive = false } = {}) {
  const structural = config?.policyVersion === "1.0.0" && config.projectName === "Lites" && config.repository === "https://github.com/erweixin/langshift.dev" && config.communityLicense === "AGPL-3.0-only" && config.commercialLicensingEnabled === true && config.claAcceptanceMethod === "signed_document" && /^https:\/\//.test(config.securityDisclosureChannel ?? "") && ["identity_required", "active"].includes(config.status);
  if (!structural) throw new Error("legal governance configuration is structurally invalid");
  const active = config.status === "active" && [config.rightsHolderLegalName, config.rightsHolderAddress, config.governingLaw, config.claSubmissionAddress].every((value) => typeof value === "string" && value.trim().length >= 3);
  if (config.status === "identity_required" && [config.rightsHolderLegalName, config.rightsHolderAddress, config.governingLaw, config.claSubmissionAddress].some((value) => value !== null)) throw new Error("inactive legal identity must not contain a partially authoritative value");
  if (requireActive && !active) throw new Error("GA requires an active legal rights holder and CLA submission authority");
  return { structural: true, active, releaseEligible: active };
}

async function main() {
  const root = resolve(import.meta.dirname, "..");
  const config = JSON.parse(await readFile(resolve(root, "config/legal-governance.json"), "utf8"));
  const result = validateLegalGovernance(config, { requireActive: process.argv.includes("--require-active") });
  const files = Object.fromEntries(await Promise.all(["CLA.md", "CONTRIBUTING.md", "TRADEMARKS.md", "SECURITY.md", "NOTICE", ".github/pull_request_template.md", "apps/web/src/components/legal-notice.tsx"].map(async (path) => [path, await readFile(resolve(root, path), "utf8")])));
  if (!files["CLA.md"].includes("Harmony Agreements") || !files["CONTRIBUTING.md"].includes("no reduced MVP path") || !files["TRADEMARKS.md"].includes("Code licences do not grant trademark rights") || !files["SECURITY.md"].includes("private vulnerability reporting") || !files.NOTICE.includes("GNU Affero General Public License") || !requiredAttestations.every((item) => files[".github/pull_request_template.md"].includes(item)) || !files["apps/web/src/components/legal-notice.tsx"].includes("github.com/erweixin/langshift.dev")) throw new Error("one or more legal governance artifacts are incomplete");
  const eventPath = process.env.GITHUB_EVENT_PATH;
  if (eventPath) {
    const event = JSON.parse(await readFile(eventPath, "utf8"));
    if (event.pull_request?.head?.repo?.fork === true) {
      if (!result.active) throw new Error("external contributions are closed until the CLA rights holder is active");
      const body = event.pull_request.body ?? "";
      if (!requiredAttestations.every((item) => body.includes(`- [x] ${item}`) || body.includes(`- [X] ${item}`))) throw new Error("external pull request is missing the complete CLA and contribution attestation");
    }
  }
  console.log(`legal governance: policy=complete activation=${config.status} releaseEligible=${result.releaseEligible}`);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main();
