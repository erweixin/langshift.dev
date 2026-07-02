# Schemas

前后端与内容管线共用的数据契约。改动规则与事件相同：先改 schema、升版本号，再改代码。

| 文件 | 用途 | 相关文档 |
| --- | --- | --- |
| `content-artifact.schema.json` | 课程内容定稿（含练习与测试）的存储/定稿校验契约。下发客户端前必须剥离 `exercise.reference_solution`；生成侧 structured output 用其简化投影 | docs/content-pipeline.md · docs/exercise-runtime.md |
| `exercise-result.schema.json` | 练习运行结果，两级运行时统一格式 | docs/exercise-runtime.md · docs/event-catalog.md |
| `review-completed.schema.json` | Review run 的 structured output，即 `ReviewCompleted` 事件 payload 主体 | docs/event-catalog.md · docs/learner-profile.md |

事件 envelope 与各事件 payload 见 `docs/event-catalog.md`（v0 先以文档为准，M0 落库时生成对应的 Go struct / TS 类型）。
