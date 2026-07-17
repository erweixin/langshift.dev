# Agent release assets

AgentWorker and ToolWorker consume four immutable files: prompts, model routes,
tool descriptors, and the provider registry. `lites-release-assets` builds all
four from one reviewed definition and emits a fifth, canonical `manifest.json`.
Every file is content addressed; the manifest is bound to the exact Git commit
and that commit's timestamp.

This directory intentionally does not contain a universal production provider
catalog. Provider model pins, prices, credential references, regions, and tool
runtime image digests are deployment-specific release inputs and must be
reviewed together with eval evidence. Test fixtures are not deployable release
definitions.

## Release procedure

1. Create a tracked definition containing every production prompt, route, tool,
   and provider pin. Prompt hashes and all container/runtime policy digests must
   already be exact. Every release serving `artifact_builder` must include the
   descriptor returned by `toolworker.ArtifactExportDescriptor` with the exact
   signed ToolWorker image digest; ToolWorker and reconciliation-worker both
   fail startup if handler coverage is incomplete.
2. Attach the definition digest to the behavior release candidate, run its
   offline eval and red-team suites, and obtain the required approval event.
3. Commit the reviewed definition. The builder rejects dirty trees and
   untracked definitions.
4. From that exact commit, run:

   ```sh
   ./scripts/build-agent-release-assets.sh \
     /absolute/repository/deploy/release/agent-assets.production.json \
     /absolute/new/output/agent-assets
   ```

5. Sign and attest `manifest.json` plus the four files in the deployment
   release system. Configure the Helm secret/CSI mount with each manifest file
   hash; never copy hashes from test values.
6. Roll out by immutable release identity. Rollback selects the previously
   approved manifest and its exact files; it never edits an existing artifact.

The builder is deterministic for a Git commit. Rebuilding the same semantic
definitions from the same commit produces byte-identical files and bundle hash.
It refuses an existing output directory and writes through a private temporary
directory followed by an atomic rename.

For the Stage 3 reference-production environment only, the strict definition
generator is documented in
[`../helm/reference-provider/README.md`](../helm/reference-provider/README.md).
It does not replace Behavior Control Plane evaluation or dual approval.
