# Lites production chart

This chart deploys the Stage 2 application control plane. PostgreSQL, NATS,
Valkey, S3, Vault, the OpenTelemetry backend, ingress, DNS and the external
secret store are cell infrastructure and are provisioned separately by
OpenTofu.

The chart intentionally cannot render from `values.yaml` alone. A release must
supply a region, cell, release version, priority class, OTLP identity, external
secret store, one immutable OCI digest per binary, per-workload secret paths,
and explicit dependency CIDRs/ports. `values.schema-test.yaml` is only a
non-deployable rendering fixture; every image uses the all-zero digest.

Each workload has a distinct `ExternalSecret` and ServiceAccount. The external
secret object must expose the file names referenced by that workload's `env`
map. The migration database Secret is provisioned before Helm because the
migration Job runs as a `pre-install,pre-upgrade` hook. No secret value belongs
in Helm values.

The namespace is default-deny for ingress and egress. DNS, OTLP, the three
internal mTLS routes and explicit external dependency CIDRs are the only
allowances. NetworkPolicy cannot express FQDN allowlists; the cell CNI or egress
gateway must additionally enforce the approved hostname/SNI policy for public
services such as the password range and SMTP endpoints.

The release requires three schedulable availability-zone domains, an External
Secrets Operator supporting `external-secrets.io/v1`, a NetworkPolicy-capable
CNI, Metrics Server or a compatible resource metrics API, and a secret reload
controller supporting the `secret.reloader.stakater.com/reload` annotation.
