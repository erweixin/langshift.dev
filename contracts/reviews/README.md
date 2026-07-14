# Stage 1 accountable approval workflow

Stage 1 is a production governance boundary, not a ceremonial checklist. No command in this repository creates approver identities, generates private keys, or infers approval from a passing test.

1. Run `npm run gate:stage1:technical` and resolve every technical failure.
2. Commit the exact contract tree. The commit must contain `gate-reports/stage-1/contract-snapshot.json` and every file named by that snapshot.
3. Run `npm run contracts:review-packet -- --source-commit <40-character-commit>`.
4. Register at least one active Ed25519 public key for each accountable role in `gate-reports/stage-1/approver-keyring.json`. The corresponding private keys must remain outside this repository and its CI artifacts.
5. Each accountable reviewer completes the role-specific checklist, saves their review evidence, and runs the signing command personally:

   `npm run contracts:approval:sign -- --role <role> --approver-id <identity> --active-role <organizational-role> --key-id <key-id> --evidence <review-file> --private-key <external-private-key.pem> --decision approved`

6. Run `npm run gate:stage1`. It repeats generation, linting and PostgreSQL isolation checks, verifies all five signatures against the frozen source commit and review packet, and regenerates the content-addressed gate report.

A rejection is signed with `--decision rejected` and always prevents promotion. Replacing an existing decision requires the explicit `--replace` flag. A contract file change creates a new content root; the old review packet and every old approval then become invalid.
