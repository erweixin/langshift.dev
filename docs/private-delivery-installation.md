# Lites Enterprise private delivery

This runbook defines the supported customer-controlled Kubernetes delivery. It is a production topology, not the hermetic `foundation.compose.yaml` development stack. Every release is installed from one immutable bundle and one image lock; mutable tags and mixed release versions are unsupported.

## Delivery boundary

The customer owns the cloud account, Kubernetes control plane, PostgreSQL, object storage, KMS/HSM, secret manager, DNS, certificates, SMTP and observability destination. Lites supplies signed multi-architecture images, Helm and OpenTofu sources, database migrations, contract schemas, SBOMs, provenance, vulnerability results and the commercial license entitlement. No plaintext customer secret or vendor support account is included.

macOS is supported for control-plane development. Firecracker installation, kernel/rootfs preparation, KVM checks and local Firecracker validation are skipped on macOS. A delivered environment that enables sandbox execution must provide separately hardened Linux KVM runtime hosts; this requirement does not apply to the macOS development workstation.

## Immutable bundle verification

1. Verify the detached signature on `bundle-manifest.json` using the offline release public key pinned in the contract.
2. Recompute every artifact SHA-256 listed by the bundle manifest before importing anything.
3. Verify each OCI image by digest, its Cosign signature and SLSA provenance. Confirm the provenance source revision and build workflow match the bundle manifest.
4. Import the image set into the customer registry without changing manifests. Generate `image-lock.json` using the destination repository and the same platform manifest digest.
5. Reject the bundle if an image lacks its CycloneDX SBOM or has an unresolved HIGH/CRITICAL vulnerability result. Do not accept mutable tags as evidence.

## Prerequisites

- Kubernetes `>=1.33 <1.36` distributed across three failure zones.
- PostgreSQL 16 with PITR, synchronous in-region HA and per-service least-privilege roles.
- Customer KMS, versioned object storage, NATS JetStream, Valkey, External Secrets and an OTLP-compatible observability stack.
- A private registry reachable by every cluster node and a customer-operated certificate authority for workload mTLS.
- Dedicated DNS names for the public application and the restricted operations entry point.
- On Linux runtime hosts only: KVM, cgroup v2, approved Firecracker/kernel/rootfs catalog and isolated runtime subnets. These checks are intentionally not run on macOS.

## Fresh installation

1. Create and lock the remote OpenTofu state backend. Apply the customer-reviewed module plan; never place PKI private keys or application peppers in state.
2. Create database roles, initialize the Store Epoch authority outside the EventStore failure domain, and write versioned workload bundles to the customer secret manager.
3. Run `lites-migrate` by digest as a one-shot identity. It must verify `deploy/migrations/manifest.json`, acquire the migration lock, apply forward migrations and run the matching `900xxx_verify_current.sql` verifier.
4. Render the Helm chart with every workload image set to a digest from `image-lock.json`, every `externalSecretRemoteKey` populated and every public origin/egress destination explicitly allowlisted.
5. Validate the render with JSON schema, kubeconform and Trivy before apply. Apply the chart and wait for all readiness probes, PodDisruptionBudgets and Store Epoch monitors to become healthy.
6. Run the signed smoke suite through the public ingress and restricted administrator ingress. Confirm tenant isolation, event append/outbox, reserve/settle/release, audit export and two-person contract approval.
7. Record cluster, database, image-lock, migration and behavior-manifest hashes in the installation evidence. Three consecutive fresh installations are required for a GA release candidate.

## Upgrade

1. Back up PostgreSQL and the Store Epoch anchor, then verify a restore in the recovery cell before changing production.
2. Verify the new bundle and compute the behavior-manifest delta. Any pilot-scoped change requires the Stage 4 equivalence or pilot rule.
3. Run expand-compatible migrations, deploy backward-compatible workers and services, then switch traffic. Destructive contract changes are forbidden in the same release.
4. Hold the previous image lock and chart values until SLOs, event lag, queue backlog, accounting reconciliation and privacy gates pass.
5. Run the post-upgrade verifier and preserve evidence. Three consecutive upgrades from the immediately previous supported release are required.

## Backup and restore

Back up PostgreSQL with continuous WAL, object versions, NATS streams, customer secret metadata, the Store Epoch anchor, Helm values, image lock and behavior manifest. Never copy plaintext application secrets into the bundle. A restore starts with a new recovery epoch, restores PostgreSQL and objects to the approved point, replays deletion tombstones, recreates streams from the database outbox where required, and only then admits commands. Validate `RPO/RTO`, accounting reconciliation and deletion receipts. Three consecutive backup and restore drills are required.

## Rollback

Application rollback uses the prior signed image lock and Helm revision only while the database remains expand-compatible. Never run a down migration against live customer data. If a release crossed an irreversible data boundary, execute forward recovery or restore into an isolated recovery cell, increment Store Epoch, validate, then cut traffic. Three consecutive rollback drills are required.

## Support handoff

The handoff contains architecture and data-flow diagrams, the exact image lock, SBOM/provenance set, configuration inventory, SLO/SLA schedule, escalation contacts, backup/restore evidence and expiry dates for the commercial license and support tier. Support access is customer-initiated, time-bounded, audited and uses customer identity; no permanent vendor credential is permitted.
