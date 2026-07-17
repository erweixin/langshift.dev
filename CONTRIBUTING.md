# Contributing to Lites

Lites is production-first software. A contribution must preserve tenant isolation, durable state-machine semantics, privacy defaults, bilingual product behavior and the single final-GA standard. There is no reduced MVP path.

## Before opening a pull request

- Read `README.md`, `docs/architecture.md`, `docs/product-implementation-plan.md`, `SECURITY.md`, `TRADEMARKS.md` and `CLA.md`.
- Do not report vulnerabilities, credentials, customer data or exploitable details in a public issue or pull request.
- External contributions are accepted only after the configured rights holder confirms a signed Individual or Entity CLA. Until `config/legal-governance.json` is active, external pull requests cannot be merged.
- Keep generated contracts, migrations, event amendments and gate reports synchronized. Never weaken a fixed threshold to make a test pass.
- Add tests for success, replay, concurrency, authorization and failure recovery in proportion to the change.
- On macOS, Firecracker installation and validation are intentionally excluded; Linux production runtime behavior and contracts must still be preserved.

## Required checks

Run the focused tests for the changed packages, contract lint, web lint/typecheck/tests where applicable, immutable Action verification and `git diff --check`. Changes to database, state-machine, profile, prompt, tool, policy, deployment or security behavior invalidate the corresponding gate evidence and require it to be regenerated on the final release candidate.

Pull requests must explain the user outcome, affected security and data boundaries, migration/rollback behavior, tests run and evidence invalidated. Reviewers may require a dedicated threat model, fault injection, bilingual evaluation or recovery drill.

## Contribution attestation

The pull-request template records that the author has an active CLA, owns or may submit the work, has disclosed third-party material, and did not include secrets or private data. This attestation is evidence of coverage; it is not a substitute for the separately signed agreement.
