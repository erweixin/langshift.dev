# Runtime host deployment

`runtime-host-agent` runs directly on dedicated Linux/KVM nodes under systemd.
It is deliberately not a Kubernetes DaemonSet: restarting a Pod normally kills
the Pod cgroup, including Firecracker children, which would invalidate the
documented process-restart adoption protocol.

Provisioning requirements:

- Linux with KVM, cgroup v2, pidfd support, a pinned network namespace and no
  general-purpose workloads on the node.
- Firecracker and jailer version `1.15.1`, immutable root-owned binaries and
  kernel/rootfs/scratch assets, with every SHA-256 digest declared in the
  environment file and verified again by the process before registration.
- A root-owned binary at `/usr/local/libexec/lites/runtime-host-agent`, the
  preflight script at `/usr/local/libexec/lites/runtime-host-preflight`, and
  the unit at `/etc/systemd/system/lites-runtime-host-agent.service`.
- Public configuration in `/etc/lites/runtime-host-agent.env` mode `0640` and
  all referenced secret files mode `0400`, owned by root.
- Firewall ingress to `8443/tcp` only from the ToolWorker security group;
  localhost-only health/metrics on `8085`; explicit egress only to PostgreSQL,
  Store Epoch Authority, Vault, S3 and the OTLP collector.
- The mTLS client leaf must contain the exact ToolWorker SPIFFE URI configured
  by `SERVER_ALLOWED_CLIENT_SPIFFE_ID`; trust in the internal CA alone does not
  authorize provisioning.

The unit uses `Delegate=yes` and `KillMode=process`. An agent crash therefore
leaves jailer-managed VMM cgroups alive for authenticated manifest + pidfd
recovery. A normal stop sends SIGTERM to the agent, which marks the host
draining, terminates owned sessions, proves cleanup, releases capacity and then
retires the host. If drain exceeds its deadline, systemd does not blindly kill
unidentified child PIDs; the next start remains in `recovering` until durable
and host-local ownership agree.

Before activation, the agent verifies every asset digest, registers the exact
host catalog and capacity as `recovering`, reconciles all signed ownership
records, and only then transitions the host to `active`.

The release workflow publishes `runtime-host-agent` as a signed, SBOM-attested
multi-architecture OCI artifact. Provisioning automation must verify the
Cosign identity and immutable image digest, extract `/lites` for the node
architecture, install it as the root-owned host binary, and record its SHA-256
in the node attestation. The OCI artifact is a distribution envelope only; it
must not be started as a container because container teardown would destroy
the cgroup ownership assumptions above.
