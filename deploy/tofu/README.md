# Official-cloud OpenTofu

`official-cloud` creates two independent production cells. Each cell is a
single-writer region spanning three availability zones and contains private EKS
capacity, Aurora PostgreSQL, encrypted Valkey, versioned KMS-backed object
storage, per-workload Secrets Manager containers, VPC flow logs, a locked backup
vault, and a DynamoDB recovery epoch CAS anchor outside the EventStore failure
domain.

The module also emits the data KMS key ARN consumed by the `nats-cell` Helm
release, grants the EBS CSI Pod Identity only the KMS operations needed for
encrypted volumes, and creates the empty `nats-server-tls` secret container.
OpenTofu does not place certificate material in state.

US and EU modules share code but do not share VPCs, databases, buckets, KMS
keys, runtime secrets, backup vaults or epoch records. Data replication between
the residency cells is intentionally absent. A later disaster-recovery apply
must target a separately approved region within the same residency boundary.

Initialize with the dedicated remote state backend and a committed dependency
lock file:

```sh
tofu -chdir=deploy/tofu/official-cloud init -backend-config=backend.hcl
tofu -chdir=deploy/tofu/official-cloud plan -out=lites.plan
tofu -chdir=deploy/tofu/official-cloud apply lites.plan
```

The Valkey tokens are sensitive inputs because the managed cache API requires
the initial credential. The remote state bucket is therefore a security
boundary: KMS encryption, versioning, locking, audit logging, MFA-protected
break-glass and no developer read access are mandatory.

OpenTofu creates empty per-workload secret containers but never invents PKI,
application peppers or database role URLs. The audited bootstrap/rotation
workflow writes those versioned bundles after least-privilege database roles,
mTLS identities and the initial epoch CAS record have been created. Application
deployment stays fail-closed until External Secrets can read complete bundles.
The dedicated Realtime role is bootstrapped and rotated with
[`runbooks/realtime-database-role.md`](../../runbooks/realtime-database-role.md);
OpenTofu only creates its empty secret container.
