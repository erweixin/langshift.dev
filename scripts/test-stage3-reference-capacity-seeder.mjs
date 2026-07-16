#!/usr/bin/env node
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { cookieValue, validateSeederConfig } from "./seed-stage3-reference-capacity.mjs";

const config = JSON.parse(await readFile("load-tests/stage3/reference-capacity-seeder.example.json"));
assert.deepEqual(validateSeederConfig(config), []);
const mutations = [
  (value) => { value.sourceCommit = "main"; },
  (value) => { value.maximumWarmupSeconds = 899; },
  (value) => { value.principals[0].connectionCount = 999; },
  (value) => { value.principals[1].hotUser = true; },
  (value) => { value.principals[0].conversationCount = 99; },
  (value) => { value.gateway.baseUrl = "http://127.0.0.1"; },
  (value) => { value.outputDirectory = "relative"; },
  (value) => { value.principals[0].passwordFile = "relative"; },
];
for (const mutate of mutations) {
  const changed = structuredClone(config);
  mutate(changed);
  assert.notDeepEqual(validateSeederConfig(changed), []);
}
const cookies = ["__Host-lites_session=session-value; Path=/; Secure; HttpOnly", "__Host-lites_csrf=csrf-value; Path=/; Secure"];
assert.equal(cookieValue(cookies, "__Host-lites_session"), "session-value");
assert.equal(cookieValue(cookies, "__Host-lites_csrf"), "csrf-value");
assert.equal(cookieValue(cookies, "missing"), "");
console.log(`stage-3 reference capacity seeder self-test: mutations=${mutations.length} cookies=3 status=passed`);
