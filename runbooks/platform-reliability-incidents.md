# Platform reliability incidents

This runbook applies to the Lites Engineering Release Candidate. Local alerts and dashboards prove that the rules load and that operators can follow the response contract; they do not prove production SLO, HA, RPO, RTO, or isolation performance.

## Common first response

1. Record the alert start time, source commit, deployment identity, `store_epoch`, affected service and bounded metric labels. Never paste prompts, credentials, user content, tenant IDs, or run IDs into metric labels.
2. Freeze non-essential releases, AI manifest changes, contract activation, Repair actions and capacity changes.
3. Preserve logs, traces, audit events and ledger evidence. Do not delete or manually rewrite event, inbox, outbox, effect-ledger or approval rows.
4. If tenant isolation, approval, duplicate side effects, terminal-state regression or lost-event integrity may be affected, stop the affected write path and declare a security/reliability incident.
5. Restore service only through versioned commands and existing reconciliation paths. Record every operator action and its approval identity.

## Zero-tolerance invariant

Applies to `LitesInvariantViolation`.

- Stop the implicated worker or API write path while leaving read-only evidence access available where safe.
- Identify the invariant label: lost event, duplicate external effect, cross-tenant access, approval bypass, or terminal-state regression.
- For cross-tenant access or approval bypass, revoke affected sessions/credentials and engage the security response owner. For duplicate external effects, freeze the tenant usage ledger and external connector before reconciliation.
- Reconcile from immutable events, idempotency records, effect ledger and audit records. Never repair projections without proving their source event and expected version.
- Reopen only after the invariant query returns zero on the repaired scope and a second approver has accepted the repair record.

## Outcome unknown

Applies to `LitesOutcomeUnknownNotConverged`.

- Pause retries for the affected effect identity. A timeout does not authorize repeating an external side effect.
- Query the provider using its idempotency key or request ID, then record a confirmed success, confirmed failure, or an explicitly unresolved terminal result in the effect ledger.
- Keep usage reservation unsettled while the outcome remains unknown. Settlement or release must reference the reconciliation evidence.
- If provider evidence is unavailable after the deadline, require two-person Repair approval before any compensating action.

## Event append or queue latency

Applies to `LitesEventAppendP99High` and `LitesInteractiveQueueP99High`.

- Compare PostgreSQL saturation, lock waits, outbox age, JetStream consumer lag and lease expiry rates for the same interval.
- Stop background workloads before changing interactive concurrency. Do not increase leases or deadlines merely to hide latency.
- Check that consumers and publishers use the current `store_epoch`. Follow [store-epoch-pitr.md](./store-epoch-pitr.md) for any recovery operation.
- Validate that no event was dropped and no command was replayed with a changed idempotency identity before clearing the incident.

## HTTP errors

Applies to `LitesHTTP5xxRatioHigh`.

- Break down by bounded `service_name`, route template and status class; raw URLs must never appear in telemetry.
- Check database, object store, Vault, NATS and downstream readiness separately. Avoid broad retries on write requests.
- Roll back only to a schema-compatible release. If a migration is implicated, use the migration manifest and its explicit rollback safety classification.

## Telemetry pipeline

Applies to `LitesTelemetryTargetMissing`.

- Treat missing telemetry as loss of operational confidence, not as zero errors.
- Check collector readiness, Prometheus target state, file-mounted rule configuration and local storage capacity.
- Keep the public status `Unknown` when current signed evidence is unavailable. Never synthesize healthy samples.

## Closure evidence

Closure requires a timeline, affected scope, immutable evidence references, root cause, corrective change, regression test, owner and follow-up date. Any claim about actual SLO, RPO, RTO, capacity, multi-AZ behavior or runtime isolation additionally requires signed production evidence from the target environment.
