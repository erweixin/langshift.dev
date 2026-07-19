import { spawnSync } from "node:child_process";
import { pathToFileURL } from "node:url";
import { resolve } from "node:path";

export function probeDockerDaemon({ command = "docker", args = ["info", "--format", "{{.ServerVersion}}"], timeoutMs = 15_000, cwd = process.cwd(), env = process.env } = {}) {
  if (!Number.isInteger(timeoutMs) || timeoutMs < 1 || timeoutMs > 60_000) throw new Error("Docker daemon probe timeout must be between 1 and 60000 milliseconds");
  return spawnSync(command, args, { cwd, env, encoding: "utf8", timeout: timeoutMs });
}

function option(name, fallback) {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] : fallback;
}

function failureReason(result) {
  if (result.error?.code === "ETIMEDOUT") return "Docker Desktop daemon did not answer within the bounded readiness timeout";
  if (result.error?.code) return `Docker daemon probe failed: ${result.error.code}`;
  return result.stderr?.trim().split("\n").at(-1) || "Docker Desktop daemon is not available";
}

function main() {
  const timeoutMs = Number(option("--timeout-ms", "15000"));
  const result = probeDockerDaemon({ timeoutMs, cwd: resolve(import.meta.dirname, "..") });
  if (result.status !== 0) {
    console.error(failureReason(result));
    process.exitCode = 1;
    return;
  }
  const version = result.stdout.trim();
  if (!version) {
    console.error("Docker Desktop daemon returned an empty server version");
    process.exitCode = 1;
    return;
  }
  console.log(`Docker Desktop daemon ready: ${version}`);
}

if (process.argv[1] && pathToFileURL(resolve(process.argv[1])).href === import.meta.url) main();
