# Production NATS cell

This release closes the cell-local JetStream dependency used by the Lites
workers. It deliberately uses the official NATS chart as a separately locked
release: `upstream.lock` fixes the repository, chart version, and archive
SHA-256, while `upstream-values.yaml` fixes the NATS 2.14.3 scratch runtime by
multi-arch OCI digest. The upstream reloader and exporter are disabled because
their current images contain fixed high/critical vulnerabilities; checksum
rollouts replace hot reload, and the restricted monitor endpoint remains
available to the observability namespace.

The scratch image currently triggers `CVE-2026-39822` solely because it was
built with Go 1.26.4. The v2.14.3 production source contains no `os.OpenRoot` or
`os.Root` use, so the vulnerable code path is unreachable. The package-scoped
exception in `.trivyignore.yaml` expires on 2026-07-31 and forces the gate to
fail unless it is removed by upgrading the upstream image or explicitly
re-reviewed.

The release is a three-node, three-zone JetStream cluster with a 200 GiB
KMS-encrypted gp3 volume per node, TLS 1.3 mutual authentication for both
clients and cluster routes, quorum-preserving disruption policy, fixed resource
requests/limits, hardened containers, and default-deny networking. The local
chart creates only the encrypted StorageClass, the TLS ExternalSecret, and the
network boundary; it never stores certificate material.

Render and security validation is mandatory before deployment:

```sh
./scripts/stage2-nats-gate.sh
```

For a cell release, copy `values.schema-test.yaml` to a non-versioned release
values file, replace the cell, region, KMS key ARN, secret store, and remote TLS
bundle key, and then install the boundary chart. Pull the upstream chart at the
locked version, verify its archive hash from `upstream.lock`, and install it
with `upstream-values.yaml`. Use `helm upgrade --install --atomic --wait` for
both releases. The data namespace, application namespace, and monitoring
namespace must already exist and carry Kubernetes's standard namespace-name
label. The TLS bundle must contain `tls.crt`, `tls.key`, and `ca.crt`; each
application workload receives its own client certificate and key through its
separate workload secret bundle. Client certificates must use the DNS SAN
`lites-outbox-publisher`, `lites-agent-scheduler`, `lites-agent-worker`,
`lites-tool-worker`, `lites-tool-reconciliation-worker`, `lites-product-worker`,
`lites-identity-import-worker`, `lites-identity-mail-worker`, or
`lites-realtime-gateway`; `verify_and_map` maps that SAN to the corresponding
least-privilege NATS user and its JetStream subject permissions.
