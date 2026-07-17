# Stage 3 reference Provider

This chart deploys the controlled OpenAI-compatible Provider used by the fixed
reference-production capacity gate. It is test infrastructure, not a customer
model endpoint. It emits deterministic token usage, a bounded hold scenario,
first-attempt 503 responses for the replay scenario, and a `reference_artifact`
tool call for the Runtime/Artifact scenario.

The LoadBalancer must receive a public address and a public DNS name whose TLS
certificate is stored in `existingSecret` as `tls.crt` and `tls.key`. This is
intentional: the production egress policy must resolve and revalidate the same
public host that appears in the immutable Provider Registry. Do not weaken SSRF
or private-address controls to reach this service. The Secret also contains a
minimum 32-byte `provider-token`; the identical value is stored in Vault at the
registry's pinned secret reference/version.

Restrict `loadBalancerSourceRanges` to the reference environment's fixed egress
addresses. The Pod has no egress permission. Three fixed replicas, zone/host
spread, a PDB, and client-IP affinity keep a retry on the same replay ledger
while distributing independent AgentWorker sources.

Before starting a formal run, verify that `https://PUBLIC_HOST/v1/` is the exact
endpoint in the release bundle, its bound host matches the certificate, and
the `reference-model-2026-07-16` wire model is pinned. A Provider rollout during
the 30-minute observation invalidates the run.

After the signed Runtime image digest and public Provider DNS are available,
generate the five-Profile reference definition (the output path must be new):

```sh
go run ./cmd/lites-reference-release \
  --output /absolute/repository/deploy/release/stage3-reference.production.json \
  --provider-host reference-provider.example.com \
  --credential-secret-ref lites/providers/stage3-reference \
  --credential-secret-version 7 \
  --runtime-image ghcr.io/organization/lites-reference-tool-runtime@sha256:FULL_DIGEST \
  --tool-worker-image ghcr.io/organization/lites-tool-worker@sha256:FULL_DIGEST
```

Review and commit that definition, then build it with
`scripts/build-agent-release-assets.sh`. Promote all five Behavior snapshots
through the Behavior Control Plane with the exact prompt/model/router/tool
hashes, fixed guardrail and Profile-definition hashes, eval report, two
signatures, rollout policy, and automatic rollback policy. The reference seeder
must run only after those production channels resolve to the expected snapshot
IDs. Test fixture hashes or a mutable image tag invalidate the capacity run.
The formal observer evidence separately records the signed tool OCI digest,
the boot kernel digest, and the bootable rootfs digest. They are different
artifacts and must never be substituted for one another.
