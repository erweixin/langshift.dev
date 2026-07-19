import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const read = (path) => readFile(resolve(root, path), "utf8");
const [prometheus, rules, compose, provisioning, dashboardSource, runbook] = await Promise.all([
  read("deploy/observability/prometheus.yaml"),
  read("deploy/observability/prometheus-rules.yaml"),
  read("deploy/compose/foundation.compose.yaml"),
  read("deploy/observability/grafana-dashboards.yaml"),
  read("deploy/observability/grafana-lites-overview.json"),
  read("runbooks/platform-reliability-incidents.md"),
]);
const dashboard = JSON.parse(dashboardSource);
const failures = [];
const assert = (condition, message) => { if (!condition) failures.push(message); };

assert(prometheus.includes("/etc/prometheus/rules/*.yaml"), "Prometheus does not load the rule directory");
for (const mount of ["prometheus-rules.yaml", "grafana-dashboards.yaml", "grafana-lites-overview.json"]) {
  assert(compose.includes(mount), `foundation Compose does not mount ${mount}`);
}
assert(provisioning.includes("/var/lib/grafana/dashboards"), "Grafana dashboard provider path is missing");

const requiredAlerts = [
  "LitesInvariantViolation",
  "LitesOutcomeUnknownNotConverged",
  "LitesEventAppendP99High",
  "LitesInteractiveQueueP99High",
  "LitesHTTP5xxRatioHigh",
  "LitesTelemetryTargetMissing",
];
for (const alert of requiredAlerts) assert(rules.includes(`alert: ${alert}`), `missing alert ${alert}`);
for (const metric of [
  "lites_http_server_requests_total",
  "lites_event_append_duration_seconds_bucket",
  "lites_queue_wait_seconds_bucket",
  "lites_tool_unknown_total",
  "lites_invariant_violations_total",
]) assert(rules.includes(metric), `rules do not reference ${metric}`);

const forbiddenLabels = [/tenant_id\s*[=,}]/, /user_id\s*[=,}]/, /run_id\s*[=,}]/, /conversation_id\s*[=,}]/, /email\s*[=,}]/];
for (const pattern of forbiddenLabels) assert(!pattern.test(rules), `unbounded or sensitive metric label found: ${pattern}`);
assert(dashboard.uid === "lites-engineering-overview", "dashboard has an unexpected stable uid");
assert(Array.isArray(dashboard.panels) && dashboard.panels.length >= 8, "dashboard does not cover the expected engineering surfaces");
const queries = dashboard.panels.flatMap((panel) => panel.targets ?? []).map((target) => target.expr ?? "").join("\n");
for (const query of ["http_5xx_ratio", "event_append", "queue_wait", "invariant_violations", "tool_outcome_unknown"]) {
  assert(queries.includes(query), `dashboard is missing ${query}`);
}
for (const section of ["Zero-tolerance invariant", "Outcome unknown", "Event append or queue latency", "HTTP errors", "Telemetry pipeline", "Closure evidence"]) {
  assert(runbook.includes(`## ${section}`), `runbook is missing ${section}`);
}
assert(runbook.includes("do not prove production SLO, HA, RPO, RTO, or isolation"), "runbook does not state the fixture/production evidence boundary");

if (failures.length) {
  for (const failure of failures) console.error(failure);
  process.exit(1);
}
console.log(`observability contract: ${requiredAlerts.length} alerts, ${dashboard.panels.length} panels, bounded-label and runbook checks passed`);
