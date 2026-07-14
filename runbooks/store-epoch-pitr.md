# EventStore PITR and store epoch rotation

This is a closed recovery procedure. It applies independently to one US or EU
cell. Never copy an EventStore, object payload, runtime secret or epoch record
between residency cells.

## Preconditions

- Incident commander, database operator and recovery operator are three named
  people. The recovery role requires MFA and is not a normal deployment role.
- Record the restore target, last known durable event time, queue depth, object
  inventory checkpoint, current image digests and the current DynamoDB epoch
  item before changing anything.
- Freeze edge writes and scale publishers, import/mail workers and all future
  command consumers to zero. Confirm no new EventStore append for five minutes.

## Rotate the independent authority first

The DynamoDB item key is `cell_id=<cell name>`. Its document contains at least
`generation_number`, `generation_id`, `created_at`, `incident_id`, `actor_arn`
and `restore_target`. `generation_id` is a newly generated UUID and is the exact
`store_epoch` exposed to applications.

1. Read the item with a strongly consistent read.
2. Generate a UUID that has never appeared in any audit record.
3. Perform one `TransactWriteItems` update conditioned on both the observed
   `generation_number` and `generation_id`. Set number `+1`, the new UUID, UTC
   creation time, incident, actor and restore target. A failed condition means
   another recovery won; stop and reread. Never retry with stale expected data.
4. Read the existing `store-epoch-authority` Secrets Manager JSON bundle,
   replace only its `store-epoch` field, and write a new secret version using
   the new UUID as the idempotency token. Do not log the bundle.
5. Wait for External Secrets and the reload controller. Verify every authority
   replica returns the new UUID over authenticated mTLS. Keep all application
   writers frozen if DynamoDB, KMS, Secrets Manager or the authority disagrees.

## Restore and reconcile

1. Restore Aurora to a new cluster at the approved point in time. Do not mutate
   the damaged cluster and do not reuse its endpoint.
2. Validate database migrations, contract checksums, object references, payload
   versions and deletion tombstones in an isolated network.
3. Atomically switch the file-backed service database URLs to the restored
   cluster. Old outbox/inbox rows retain their old UUID and therefore cannot be
   published or claimed under the new authority.
4. Quarantine or purge JetStream messages from the prior generation. Rebuild
   still-legal commands from current EventStore projections with new command
   IDs, causation links and the new UUID.
5. Open one publisher shard and one consumer replica. Confirm old-generation
   rejection audit events, zero duplicate effects and converged outbox/inbox
   counts. Then restore consumers, publishers and finally edge writes in stages.

## Completion evidence

Attach the DynamoDB before/after images, Secrets Manager version IDs, Aurora
restore time, database/object checksums, old-command rejection count, recreated
command manifest, actual RPO/RTO and three-person approval to the incident. A
database that is readable but has missing referenced objects, mismatched epoch,
incomplete deletion cleanup or unreconciled commands is not recovered.
