# Lites Architecture Notes

This directory keeps the architecture kernel for Lites plus one standalone UX
prototype. Frontend code, backend code, generated schemas, local scripts, and
product-specific implementation notes were removed so the project can restart
from the cloud-agent architecture instead of carrying a premature
implementation.

## Reading Path

| Order | Document | Purpose |
| ---: | --- | --- |
| 0 | [architecture.md](./architecture.md) | Global model, core loop, terms, logical architecture, and Lite v1 shape |
| 1 | [end-to-end-flow.md](./end-to-end-flow.md) | Component responsibilities, data flow, execution sequence, and failure paths |
| 2 | [state-machines.md](./state-machines.md) | Run, ToolCall, and Command state transitions and invariants |
| 3 | [concurrency-and-durability.md](./concurrency-and-durability.md) | EventStore, append contract, CAS, outbox/inbox, and recovery semantics |
| 4 | [execution-model.md](./execution-model.md) | Queue scheduling, worker transactions, effects, LLM calls, and joins |
| 5 | [agent-execution-kernel.md](./agent-execution-kernel.md) | Reliable run/job/worker kernel boundaries and handler contract |
| 6 | [agent-safety-and-guardrails.md](./agent-safety-and-guardrails.md) | Agent safety, tool approval, prompt-injection defense, and output checks |
| 7 | [tool-system.md](./tool-system.md) | Tool descriptors, registration, discovery, versioning, and lifecycle |
| 8 | [llm-provider.md](./llm-provider.md) | Provider interface, routing, fallback, circuit breaking, and cost tracking |
| 9 | [memory.md](./memory.md) | Memory tiers, storage, retrieval, pruning, and tenant isolation |
| 10 | [orchestration-patterns.md](./orchestration-patterns.md) | Multi-agent delegation, supervision, pipelines, and checkpoints |
| 11 | [realtime.md](./realtime.md) | Realtime delivery, reconnection, slow consumers, and token streaming |
| 12 | [runtime-and-sandbox.md](./runtime-and-sandbox.md) | Runtime threat model, isolation tiers, secret broker, and workspace ownership |
| 13 | [multi-tenancy-and-security.md](./multi-tenancy-and-security.md) | Tenant isolation, permissions, data retention, deletion, and repair commands |
| 14 | [operations.md](./operations.md) | Sweeper behavior, observability, fault injection, and invariant tests |
| 15 | [capacity-and-scaling.md](./capacity-and-scaling.md) | Load vectors, scaling paths, and readiness standards |
| 16 | [roadmap.md](./roadmap.md) | Technical evolution order |

## UX Prototype

[ux-prototype.html](./ux-prototype.html) is retained as a standalone product
exploration artifact. It can be opened directly in a browser and used to reason
about the user journey, but it is not frontend source code and should not drive
architecture decisions unless those decisions are reflected back into the docs
above.

## Removed Intentionally

The removed documents were either product-specific, implementation-status
specific, or tied to the deleted frontend/backend slice:

- content, curriculum, learner profile, exercise runtime, and growth docs
- implementation plan/status docs
- frontend-oriented product blueprint docs
- generated schema notes and local development scripts

If those topics become active again, recreate them from the architecture kernel
instead of restoring the old implementation slice wholesale.
