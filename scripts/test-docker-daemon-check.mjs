import assert from "node:assert/strict";
import { probeDockerDaemon } from "./check-docker-daemon.mjs";

const success = probeDockerDaemon({ command: process.execPath, args: ["-e", "process.stdout.write('test-daemon')"], timeoutMs: 1_000 });
assert.equal(success.status, 0);
assert.equal(success.stdout, "test-daemon");

const timeout = probeDockerDaemon({ command: process.execPath, args: ["-e", "setTimeout(() => {}, 10000)"], timeoutMs: 50 });
assert.equal(timeout.status, null);
assert.equal(timeout.error?.code, "ETIMEDOUT");
assert.throws(() => probeDockerDaemon({ timeoutMs: 0 }), /between 1 and 60000/);

console.log("Docker daemon probe self-test passed: ready response accepted; hung daemon and invalid timeout rejected");
