# Agent Guidelines

Lites is a production-first Cloud Agent product under active implementation.
Treat `README.md`, `docs/architecture.md`, `docs/ux-prototype.html`, and
`docs/product-implementation-plan.md` as the product, architecture, UX, and
fixed-gate implementation authorities.

## Working Rules

- Preserve the Lite-first Cloud Agent semantics described in
  `docs/architecture.md`.
- Treat `docs/ux-prototype.html` as a standalone product exploration artifact,
  not as frontend source code.
- Build toward the complete GA scope. Internal stages are engineering gates,
  not MVP or reduced-scope product releases.
- Preserve machine-verifiable evidence for every fixed gate and never infer an
  approval or waive a zero-tolerance invariant.
- Prefer explicit boundaries for API, event store, command queue, workers,
  durable effects, repair, and observability when implementation resumes.
