# Lites production chart

This chart deploys the production web application and application control plane. PostgreSQL, Valkey,
S3, Vault, the OpenTelemetry backend, ingress, DNS and the external secret
store are cell infrastructure. NATS is deployed by the separately locked
`../nats-cell` release on top of the OpenTofu cell.

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

The namespace is default-deny for ingress and egress. The edge namespace can
reach only the web application and public API gateway. The web application can
reach only the API gateway and OTLP collector; its API trust root is mounted
from its own `ExternalSecret`. DNS, OTLP, internal mTLS routes,
selector-scoped NATS access and explicit external
dependency CIDRs are the only allowances. NetworkPolicy cannot express FQDN allowlists; the cell CNI or egress
gateway must additionally enforce the approved hostname/SNI policy for public
services such as the password range and SMTP endpoints.

The release requires three schedulable availability-zone domains, an External
Secrets Operator supporting `external-secrets.io/v1`, a NetworkPolicy-capable
CNI, Metrics Server or a compatible resource metrics API, and a secret reload
controller supporting the `secret.reloader.stakater.com/reload` annotation.

The `web-app` image is a standalone Next.js server in a pinned distroless Node
runtime. `/live` and `/ready` share port 3000 with the application so the same
process that serves traffic is probed. The edge must preserve same-origin
`/api/v1/*` and `/api/v1/realtime` traffic; the server proxies those requests to
the API gateway without caching writes or SSE responses.
