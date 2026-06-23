# Agent Guidelines

Lites is a lite version of a production-grade cloud Agent platform.

The implementation should stay small and dependency-conscious, but the core
workflow must follow production best practices. A feature may use a simplified
local implementation, but it should preserve the same boundaries and semantics
that a real cloud deployment needs.

## Product Direction

- Build a cloud Agent system with clear request, task, run, and result flows.
- Prefer simple implementations, but avoid demo-only shortcuts in core flows.
- Keep production concerns visible from the beginning: message queue, access
  control, durable state, tenant isolation, auditability, and operational
  observability.
- Design lite components so they can later be replaced by managed services
  without rewriting business logic.

## Dependency Policy

- Prefer standard library and small local implementations by default.
- Add third-party packages only when they meaningfully improve correctness,
  security, maintainability, or production readiness.
- Keep infrastructure choices behind simple interfaces so lite implementations
  can later be replaced by managed services.

## Architecture Rules

- Keep clear boundaries between API, auth, queue, worker, persistence, and audit
  concerns.
- Do not run long-lived Agent tasks directly inside request handlers; persist and
  enqueue work instead.
- Model core production practices explicitly when needed: task lifecycle,
  retries, permissions, persistence, tenant isolation, and audit logs.
- Prefer small interfaces and simple implementations that can evolve without
  becoming a framework.

## Implementation Style

- Favor simple, readable code over broad abstractions.
- Keep implementations scoped to the current need.
- Use small local helpers when they make the code clearer.
- For backend code, prefer Go standard library packages first.
- For frontend code, prefer the simplest viable browser and npm setup before
  adopting larger frameworks or tooling.

## Project Shape

- `backend/` is a Go project.
- `frontend/` is a Vite and React npm project.
- `docs/architecture.md` describes the cloud Agent architecture and production
  semantics.
- The root package coordinates project-level scripts.
