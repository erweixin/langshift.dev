# Account erasure operations

This runbook governs irreversible subject erasure in one residency cell. An
account is not reported erased until all six surfaces have durable receipts:
payload, memory, indexes, workspace/artifact, cache and snapshot. Event and
security audit rows retain only pseudonymous identifiers, hashes and invalid
references; they must never retain recoverable subject content.

## Provisioning contract

- Provision `lites_erasure_worker` before migration 84 with `LOGIN`,
  `NOINHERIT` and `NOBYPASSRLS`. Migration 84 grants only the tables and
  security-definer scans required by the worker. Use its own
  `DATABASE_URL_FILE`; never reuse an owner or migration role. If the role is
  created after the migration, apply the exact grants from migration 84 and
  rerun `900084_verify_current.sql` before enabling the workload.
- Enable object versioning on both `S3_PAYLOAD_BUCKET` and
  `S3_ARTIFACT_BUCKET`. The worker principal needs prefix-scoped object read,
  `ListBucketVersions` and `DeleteObjectVersion` permissions. A delete marker
  alone is not evidence: the worker lists every version and delete marker,
  removes them by version ID, relists the key and requires an empty result.
- Give the Vault workload identity read access to payload key metadata and the
  exact Transit management paths used by `VAULT_MEMORY_TRANSIT_MOUNT`. It must
  be allowed to set `deletion_allowed=true`, delete the per-subject key and
  verify the key endpoint is absent. Do not grant mount-wide administration or
  reuse the token of another workload.
- Give the Valkey identity access only to the `lites:subject:*` erasure tag sets
  and their tagged cache keys, including `SMEMBERS`, `UNLINK`, `DEL` and the
  approved atomic Lua script. All content-bearing cache writers must register
  a key in the subject tag set in the same operation that makes it readable.
- Supply three independent base64 keys, each at least 32 decoded bytes:
  `ERASURE_INBOX_LEASE_PEPPER_FILE`, `ERASURE_IDENTITY_KEY_FILE` and
  `ERASURE_RECEIPT_KEY_FILE`. Rotate through a reviewed dual-read procedure;
  replacing the receipt key without preserving replay verification breaks the
  evidence chain.
- The NATS identity may consume only the account-erasure command subject and
  publish no application command. Keep `NATS_STREAM_REPLICAS>=3` and the
  durable consumer name stable across ordinary rollouts.

## Normal deletion

1. The authenticated account API creates one request and schedules its outbox
   command at the 30-day `scheduled_for` time. Cancellation is allowed only
   before that boundary. Confirm `agent.outbox.available_at` equals the request
   timestamp; never publish the command early.
2. At the deadline the worker claims the request and pseudonymizes the global
   principal, removes credentials/tokens/sessions, withdraws consent and marks
   memberships left. It then removes every referenced object version, subject
   key, index derivative, artifact, cache entry and snapshot.
3. Each surface writes an append-only, tenant-scoped receipt for the current
   Store Epoch. Completion requires exactly six verified receipts, one
   immutable tombstone and one `SubjectErasureCompleted` event in the same
   database transaction.
4. Support may report completion only after the request is `completed`, the
   six receipt hashes recompute to the manifest hash, object/key verification
   is absent, and the event outbox has published. A partial receipt set remains
   an active incident and must not be described as successful deletion.

## Restore and PITR

Keep `account-erasure-worker` scaled to zero while the database/object restore
is isolated. Rotate Store Epoch first according to `store-epoch-pitr.md`, then
start one worker replica. Its initial reconciliation scans immutable
tombstones missing any of the six receipts for the new epoch and re-applies all
surfaces. Do not open reads, writers, index rebuilders or public traffic until
every tombstone has six current-epoch receipts. Memory admission also rejects
new writes and retrieval manifests for tombstoned subjects, preventing a
restored projection from making erased material readable.

## Alerts and evidence

Alert on any delivery error, retry exhaustion, incomplete receipt set older
than 15 minutes after first claim, current-epoch tombstone deficit, readiness
failure, Store Epoch mismatch, object version remaining after purge, or Vault
key still readable after deletion. Preserve request/tombstone IDs, epoch,
surface receipt hashes, manifest hash, event ID and dependency audit request
IDs. Never attach email addresses, plaintext reasons, payloads, Vault tokens or
object contents to an incident.

Before a release candidate, run the PostgreSQL 16 100-subject gate and verify
600 initial receipts, 100 completion events/tombstones, zero readable subject
surfaces, then rotate to a new recovery epoch and verify 600 additional
receipts. Production restore drills require the same invariant on customer-like
versioned object storage, Vault and Valkey, not only test doubles.
