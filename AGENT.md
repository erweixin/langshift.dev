# Agent Guidelines

Lites is currently an architecture-first Cloud Agent exploration.

The repository intentionally keeps the implementation surface small. Treat
`docs/architecture.md` as the source of direction for future work, and avoid
adding frontend or backend code until the next implementation slice is chosen.

## Working Rules

- Preserve the Lite-first Cloud Agent semantics described in
  `docs/architecture.md`.
- Treat `docs/ux-prototype.html` as a standalone product exploration artifact,
  not as frontend source code.
- Keep future implementation slices small and reversible.
- Do not reintroduce broad scaffolding until there is a clear product or
  architecture reason.
- Prefer explicit boundaries for API, event store, command queue, workers,
  durable effects, repair, and observability when implementation resumes.
