# LearnQ 实施计划

本计划以“每个阶段均可构建、可测试、可解释”为约束。正式构建始终使用 `GOWORK=off`。

## P0：可靠异步任务闭环

- SQL migration、MySQL repository 与事务内写入 study record/module/task/outbox。
- Redis ZSet + Redis TIME + Lua 原子领取，Dispatcher、Worker Pool、Reaper、Reconciler。
- 状态机、fencing lease token、三次尝试、2s/4s 退避、人工 retry generation。
- Fake AI、JSON Schema 校验、报告原子导出、复习间隔与事件。
- API、基础 Web、单元及依赖集成测试。

## P1：Skill、模型与 Multi-Agent

- 版本化 Prompt/Schema、五个 Skill Registry。
- 确定性 Coordinator；Review/Algorithm/Interview/Planner 工作流。
- Fake/Real 模型共用业务链，记录 agent run/step/tool/retrieval/token trace。
- weekly-plan 的事实由 SQL 统计，模型仅组织表达。

## P2：异步 RAG 与离线评估

- Markdown/TXT/JSON 上传限制、状态机、rune/行号安全切块。
- 确定性 Fake dense、OpenAI-compatible Real embedding、hashed lexical sparse。
- Qdrant named vectors 与 dense/sparse prefetch + RRF；可重建索引与来源过滤。
- 引用约束与无证据错误；三组 Recall@K/NDCG/引用覆盖率。

## P3：交付与复现

- 中文 Go Template + 原生 JS 页面、Compose、Makefile、CI。
- MySQL/Redis/Qdrant 集成与端到端故障验收脚本。
- README、AQ 编号验收报告、15 章从零复现文档。

## 阶段门禁

每阶段运行：`gofmt`、`GOWORK=off go build ./...`、`GOWORK=off go test ./...`；依赖可用时运行集成/验收。失败先修复，再更新状态和矩阵。
