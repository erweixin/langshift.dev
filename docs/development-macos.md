# macOS development boundary

The supported local development environment is macOS with Docker Desktop. It
builds and verifies the web application, control-plane services, PostgreSQL,
NATS, Valkey, object storage, Vault, contracts, migrations, Helm resources and
Linux OCI images.

Firecracker is intentionally outside the macOS local gate. Local setup and
acceptance scripts must not require KVM, a Firecracker binary, a guest kernel,
a root filesystem, jailer installation or runtime-host preflight. The
production Linux runtime-host implementation remains in the repository and is
validated only on dedicated Linux/KVM infrastructure and its release pipeline.

Skipping that host-only supply chain on macOS is an environment boundary, not
a production fallback. Product flows must use a configured remote runtime cell
when they require execution of untrusted user code; they must report the
runtime dependency as unavailable if no such cell exists.
