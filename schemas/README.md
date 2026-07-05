# Schemas

前后端与内容管线共用的数据契约。改动规则与事件相同：先改 schema、升版本号，再改代码。

| 文件 | 用途 | 相关文档 |
| --- | --- | --- |
| `content-artifact.schema.json` | 课程内容定稿（含练习与测试）的存储/定稿校验契约。下发客户端前必须剥离 `exercise.reference_solution`；生成侧 structured output 用其简化投影 | docs/content-pipeline.md · docs/exercise-runtime.md |
| `content-generation-input.schema.json` | `POST /api/content-generation-runs` 请求体；当前为调试入口，后续 task-scoped 入口复用同一组生成维度 | docs/implementation-plan.md · docs/content-pipeline.md |
| `content-generation-run.schema.json` | 内容生成 run 的公开投影，供创建响应、轮询和前端状态机共用 | docs/implementation-plan.md |
| `learning-outline.schema.json` | 用户个性化学习大纲；它产出 TaskSpec，决定后续生成哪些可复用内容 | docs/agent-execution-kernel.md · docs/product-to-platform.md |
| `outline-generation-input.schema.json` | `POST /api/outline-generation-runs` 请求体；用户信息和用户要求在这里影响路线，而不是直接影响 `content_artifacts` | docs/agent-execution-kernel.md |
| `outline-generation-run.schema.json` | 学习大纲生成 run 的公开投影 | docs/agent-execution-kernel.md |
| `public-content-artifact.schema.json` | `GET /api/content-artifacts/{content_key}` 下发给客户端的公开投影，明确不含 `exercise.reference_solution` | docs/exercise-runtime.md |
| `exercise-result.schema.json` | 练习运行结果，两级运行时统一格式 | docs/exercise-runtime.md · docs/event-catalog.md |
| `review-output.schema.json` | Review run 传给模型的 structured output，不含调用方注入字段 | docs/event-catalog.md · docs/learner-profile.md |
| `review-completed.schema.json` | `ReviewCompleted` 事件 payload 主体，等于模型输出 + 调用方注入的 `evidence_id` | docs/event-catalog.md · docs/learner-profile.md |

事件 envelope 与各事件 payload 见 `docs/event-catalog.md`（v0 先以文档为准，M0 落库时生成对应的 Go struct / TS 类型）。
