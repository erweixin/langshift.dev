import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { mkdir, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const args = new Map();
for (let index = 2; index < process.argv.length; index += 2) args.set(process.argv[index], process.argv[index + 1]);
const sourceCommit = args.get("--source-commit");
const output = args.get("--output");
if (!/^[0-9a-f]{40}$/.test(sourceCommit ?? "") || !output) throw new Error("--source-commit and --output are required");

const git = (...values) => execFileSync("git", values, { cwd: root });
git("cat-file", "-e", `${sourceCommit}^{commit}`);
const tracked = git("ls-tree", "-r", "--name-only", sourceCommit).toString("utf8").trim().split("\n").filter(Boolean);
const createdAt = git("show", "-s", "--format=%cI", sourceCommit).toString("utf8").trim();
const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const groups = [
  { kind: "frontend_chunk", id: "workspace-routes", prefixes: ["apps/web/src/app/"], pilotScope: true },
  { kind: "frontend_chunk", id: "shared-ui-components", prefixes: ["apps/web/src/components/", "apps/web/src/lib/", "apps/web/src/i18n/"], pilotScope: true },
  { kind: "shared_dependency", id: "web-dependency-lock", exact: ["package.json", "package-lock.json", "apps/web/package.json"], pilotScope: true },
  { kind: "api_contract", id: "openapi-chain", prefixes: ["contracts/openapi/"], pilotScope: true },
  { kind: "event_contract", id: "event-chain", prefixes: ["contracts/events/"], pilotScope: true },
  { kind: "model", id: "provider-and-routing-contract", prefixes: ["internal/llmgateway/", "internal/behavior/"], exact: ["contracts/catalog/profile-contracts.json"], pilotScope: true },
  { kind: "prompt", id: "prompt-artifact-contract", exact: ["internal/agentworker/prompt_artifact.go"], prefixes: ["agent-definitions/"], pilotScope: true },
  { kind: "tool", id: "tool-contracts", prefixes: ["internal/toolregistry/", "internal/toolworker/"], pilotScope: true },
  { kind: "profile", id: "five-agent-profiles", prefixes: ["contracts/profiles/", "profile-evals/"], pilotScope: true },
  { kind: "policy", id: "guardrail-privacy-egress", prefixes: ["internal/security/guardrail/", "internal/identity/erasure/", "cmd/account-erasure-worker/", "contracts/events/amendments/v1.24.0/"], exact: ["internal/llmgateway/egress/policy.go", "internal/runtime/policy.go", "internal/identity/postgres/account_erasure_dispatcher.go", "internal/identity/postgres/account_erasure_store.go", "internal/identity/postgres/account_erasure_surface.go", "internal/memory/postgres/inline_write.go", "internal/memory/postgres/retrieval_manifest.go", "internal/objectstore/s3store/store.go", "internal/objectstore/s3store/purge_router.go", "internal/payload/vaultkeys/purger.go", "contracts/catalog/privacy-contract.json"], pilotScope: true },
  { kind: "ontology", id: "career-ontology", exact: ["product-content/releases/1.0.0/career-ontology.json"], pilotScope: true },
  { kind: "content", id: "product-content-release", prefixes: ["product-content/releases/"], pilotScope: true },
  { kind: "feature_flag", id: "release-feature-configuration", exact: ["deploy/helm/lites/values.yaml", "deploy/helm/lites/values.schema.json"], pilotScope: true },
];

const components = groups.map((group) => {
  const files = tracked.filter((file) => group.exact?.includes(file) || group.prefixes?.some((prefix) => file.startsWith(prefix))).sort();
  if (!files.length) throw new Error(`behavior component ${group.kind}:${group.id} has no tracked inputs`);
  const digest = createHash("sha256");
  for (const file of files) {
    const content = git("show", `${sourceCommit}:${file}`);
    digest.update(`${file}\0${content.length}\0`);
    digest.update(content);
  }
  return { kind: group.kind, id: group.id, hash: digest.digest("hex"), pilotScope: group.pilotScope, inputs: files };
});
const impactRules = JSON.parse(git("show", `${sourceCommit}:behavior-manifests/impact-matrix.json`).toString("utf8"));
const base = { manifestVersion: "1.0.0", sourceCommit, createdAt: new Date(createdAt).toISOString(), components, impactRules };
const manifest = { ...base, manifestHash: sha256(JSON.stringify(base)) };
const target = resolve(root, output);
await mkdir(dirname(target), { recursive: true });
await writeFile(target, `${JSON.stringify(manifest, null, 2)}\n`);
console.log(`behavior manifest: components=${components.length} commit=${sourceCommit} hash=${manifest.manifestHash}`);
