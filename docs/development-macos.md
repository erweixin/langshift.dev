# macOS development boundary

The supported local development environment is macOS with Docker Desktop. It
builds and verifies the web application, control-plane services, PostgreSQL,
NATS, Valkey, object storage, Vault, contracts, migrations, Helm resources and
Linux OCI images.

The hermetic product journey is started and torn down by:

```bash
npm run local:product:e2e
```

The command builds the repository images, generates ephemeral purpose-separated
credentials and a local CA, starts `foundation.compose.yaml` together with
`local-product.compose.yaml`, runs the desktop and mobile no-Demo Playwright
journey, and performs a PostgreSQL backup/restore smoke. It writes classified
evidence under `.tmp/verification/` and always removes its project-scoped
containers and volumes. `npm run verify:macos` invokes this same journey after
the Go, Web, contract and database gates; callers do not pre-start Mailpit or
manually inject a mock API.

The local topology includes the real `realtime-gateway`; the API Gateway must
not route `/v1/realtime` to Product Service as a placeholder. Reconnects use
the public `after_seq` cursor and consume named SSE `event` frames. Review
recovery remains product-owned: the Web app polls the durable daily-task
snapshot and never needs direct access to the internal `/v1/runs/{id}` control
surface.

The product-stack report separates `executionResult` from releasable evidence.
A successful journey on a dirty worktree records the execution as `passed`,
but its report `result` remains `failed` with
`evidenceClassification=current_dirty`. This allows development diagnosis
without letting a dirty report satisfy Engineering RC policy.

For a failed local diagnosis only, `KEEP_MACOS_PRODUCT_STACK=true npm run
local:product:e2e` preserves the project and prints its project/work paths.
Those paths contain ephemeral credentials and must be torn down after
inspection; the default remains fail-safe automatic cleanup.

The gate reserves a 2 GiB host working-space safety floor for generated
artifacts and its database backup. Docker Desktop manages image and build-cache
capacity separately, so this check is not presented as proof that a cold image
build will fit; Docker failures remain classified product-stack failures.
The gate checks the host floor before contacting Docker, writes a current-commit
`failed` verification report, and does not start containers when it is not met.
`LITES_MACOS_MIN_FREE_GIB` may raise this threshold for a larger local Docker
Desktop allocation, but must be an integer from 1 through 64.
The subsequent Docker daemon readiness probe has a 15-second hard timeout, so
a running but wedged Docker Desktop produces a classified
`docker_daemon_unavailable` report instead of hanging the Engineering RC gate.
Compose work is intentionally serialized because Docker
Compose 2.35.1 can crash while concurrently pulling the overlapping foundation
layers; cached later runs remain substantially faster.

The matching Playwright Chromium build is also required. Install it once with
`npx playwright install chromium`; the product gate launches and closes the
headless browser during preflight and reports `playwright_browser_unavailable`
before building images when the binary is missing or cannot run.

Engineering RC verification requires network access to GitHub's public REST
API. `GITHUB_TOKEN` or `GH_TOKEN` is optional for a public repository and is
used when present to increase the rate limit; an invalid token falls back to
the same read-only unauthenticated request. `verify:macos` captures every issue
through bounded, ordered pagination, excludes pull requests, requires every
open issue to have exactly one configured severity label, and fails when any
P0, P1 or unclassified issue remains. The snapshot is bound to the same clean
commit, expires after 24 hours and is written only under `.tmp/verification/`;
a partial, hand-authored or stale issue list is not accepted.

`npm test` and `npm run contracts:lint` validate the generated current
engineering contract set. The old frozen Stage 1 evidence check is retained as
the explicit `npm run contracts:lint:historical-stage1` command; it is not
current RC evidence and is not part of the normal developer test loop.

`npm run contracts:generate` applies the frozen base contract and every ordered
file under `contracts/openapi/amendments/` to generate
`contracts/openapi/lites.current.openapi.json`. The Web schema at
`apps/web/src/lib/api/schema.d.ts` is generated from that current artifact, not
from the frozen base alone. The validator rejects unresolved references,
equivalent templated paths such as `{id}` versus `{project_id}`, an amendment
overlay that drifted from the generated operation, and any literal Web `/v1`
path absent from the current contract. Both generated files are build outputs;
contract changes belong in the base generator or a monotonic amendment.

The task journey is also refresh-safe. `GET /v1/daily-tasks` returns an
owner-scoped `review_recovery` snapshot that binds the current immutable
submission to the latest evaluator generation, Run, Review, and Evidence.
After an interrupted browser session the Web app resumes polling from that
snapshot, fetches a completed Review, or explicitly retries a failed generation
without creating a second submission. Migration 95 permits retry history while
enforcing at most one generating or succeeded evaluator generation per
submission and rubric. PostgreSQL integration tests remain part of the macOS
gate; compiling them without a database is not passing evidence.

The database gate uses a three-minute per-package timeout because the complete
deterministic cancellation and approval fault suite legitimately exceeds one
minute on macOS. Migration smoke still runs three full up/down cycles. The
erasure-only locale tombstone `und` is introduced by migration 99; public
account mutations remain restricted to `en` and `zh-CN`.

The local model and mailbox processes are compiled from this repository and are
hard-locked to `engineering-test` plus `LITES_LOCAL_COMPOSE=true`. Their output
is fixture evidence only. The browser journey does not execute untrusted user
code; runtime state machines and deterministic failure adapters are verified by
the Go gate. Neither path proves Firecracker isolation.

Firecracker is intentionally outside the macOS local gate. Local setup and
acceptance scripts must not require KVM, a Firecracker binary, a guest kernel,
a root filesystem, jailer installation or runtime-host preflight. The
production Linux runtime-host implementation remains in the repository and is
validated only on dedicated Linux/KVM infrastructure and its release pipeline.

`npm run verify:production-contracts` does execute the rejecting fixture test
for `contracts/runtime/linux-firecracker-host.schema.json`. It binds the Linux
host contract to the systemd unit, preflight, environment, entrypoint and
Firecracker jailer adapter, and rejects fixtures that omit KVM, cgroup v2,
asset digests, fail-closed restart recovery or the macOS exemption. This check
is deliberately classified as `fixture_validated`: it does not run the
preflight, boot a microVM or measure isolation strength.

Skipping that host-only supply chain on macOS is an environment boundary, not
a production fallback. Product flows must use a configured remote runtime cell
when they require execution of untrusted user code; they must report the
runtime dependency as unavailable if no such cell exists.
