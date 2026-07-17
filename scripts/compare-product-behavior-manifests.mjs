import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const args = new Map();
for (let index = 2; index < process.argv.length; index += 2) args.set(process.argv[index], process.argv[index + 1]);
const pilotPath = args.get("--pilot");
const rcPath = args.get("--rc");
const output = args.get("--output");
if (!pilotPath || !rcPath || !output) throw new Error("--pilot, --rc, and --output are required");
const pilot = JSON.parse(await readFile(resolve(root, pilotPath), "utf8"));
const rc = JSON.parse(await readFile(resolve(root, rcPath), "utf8"));
const hash = (value) => createHash("sha256").update(JSON.stringify(value)).digest("hex");
const rcIndex = new Map((rc.components ?? []).map((item) => [`${item.kind}:${item.id}`, item]));
const compared = (pilot.components ?? []).filter((item) => item.pilotScope).map((item) => {
  const current = rcIndex.get(`${item.kind}:${item.id}`);
  return { kind: item.kind, id: item.id, pilotHash: item.hash, rcHash: current?.hash ?? null, status: current?.hash === item.hash ? "equivalent" : current ? "changed" : "missing" };
});
const changed = compared.filter((item) => item.status !== "equivalent");
const base = { reportVersion: "1.0.0", kind: "pilot-rc-behavior-equivalence", generatedAt: new Date().toISOString(), pilotManifestHash: pilot.manifestHash, rcManifestHash: rc.manifestHash, status: changed.length ? "failed" : "passed", summary: { compared: compared.length, equivalent: compared.length - changed.length, changed: changed.length }, components: compared };
const report = { ...base, reportHash: hash(base) };
const target = resolve(root, output);
await mkdir(dirname(target), { recursive: true });
await writeFile(target, `${JSON.stringify(report, null, 2)}\n`);
console.log(`${report.status}: equivalent=${report.summary.equivalent}/${report.summary.compared} report=${report.reportHash}`);
if (changed.length) process.exitCode = 1;
