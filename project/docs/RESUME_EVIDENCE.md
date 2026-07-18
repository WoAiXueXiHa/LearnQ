# LearnQ 简历表述与代码证据

> 用途：保证简历、README、代码和面试口述使用同一套事实。
> 原则：只有能定位代码、运行测试或现场演示的内容，才写成“实现”。

## 推荐项目标题与技术栈

**LearnQ｜AI 驱动的学习任务调度系统（个人项目）**

技术栈：

```text
Go、Gin、Eino Compose、MySQL、Redis ZSet/Lua、Qdrant、RAG、Docker Compose
```

当前代码使用 Eino Compose 构建工作流，没有使用 Eino ADK 的 Agent/Handoff 运行时，因此简历技术栈不要写成“Eino ADK / Compose”。

## 推荐项目简介

面向长期学习场景构建的本地单用户 AI 学习任务系统，将学习记录异步转化为复盘报告和延迟复习任务，并提供版本化 Skill、可观察的 Multi-Agent 编排实验、文档混合检索和离线评估。

## 六条推荐描述

### 1. 确定性 Multi-Agent 编排实验

推荐写法：

> 基于 Eino Compose 构建可观察的 Multi-Agent 手动实验：由 Go 根据输入模块确定性选择 Review、Algorithm、Interview 路由，各节点并行输出独立结果，末端 Planner 节点统一展示并独立生成周计划；限制模型控制流，记录每个 Agent 的错误和运行 ID。

代码证据：

- `internal/skill/eino_workflow.go`
- `internal/skill/registry_test.go`
- `internal/api/operations.go`

演示：

```bash
curl -fsS -X POST http://127.0.0.1:8080/api/v1/skills/multi-agent/runs \
  -H 'Content-Type: application/json' \
  -d '{"title":"算法复盘","modules":["algorithm"]}'
```

边界：

- 不参与异步报告主链；
- Planner 没有把前序 Agent 文本再次喂给模型；
- 不描述为自治 Agent 团队或真正的语义聚合器；
- 不写 Eino ADK。

### 2. 混合 RAG 检索

推荐写法：

> 构建文档异步索引与混合 RAG 链路：按标题、行号、顺序和 SHA-256 保存 chunk 元数据，使用 Qdrant 执行 dense 与 hashed lexical sparse 双路召回并通过 RRF 融合；Chat 模型仅基于召回证据生成回答，服务端校验 `[S#]` 引用，使用 Recall@K、NDCG 和引用覆盖率评估检索结果。

代码证据：

- `internal/rag/chunk.go`
- `internal/rag/qdrant.go`
- `internal/rag/metrics.go`
- `internal/indexer/indexer.go`
- `internal/evaluation`

测试证据：

- `internal/rag/*_test.go`
- `internal/indexer/indexer_integration_test.go`
- `scripts/acceptance.sh`

边界：

- sparse 是英文 token + 中文 unigram/bigram + FNV-1a + log-TF，不称为 BM25；
- Fake 模式的评估是 pipeline test，不等于真实模型质量 benchmark；
- 未使用 Cross-Encoder reranker。

### 3. 可插拔 Skill

推荐写法：

> 设计版本化 Skill Registry，将每日复盘、算法诊断、面试追问、项目讲解和周计划封装为带元数据、输入/输出 JSON Schema、Prompt 版本和 Tool 白名单的能力；执行时校验模型结构化输出并记录 Tool 请求、响应、引用和延迟。

代码证据：

- `internal/skill/registry.go`
- `internal/skill/prompts`
- `internal/skill/schemas`
- `internal/trace/recorder.go`

边界：

- Registry 是编译进二进制的静态注册表；
- 不支持运行时下载第三方代码；
- 当前 common input Schema 只约束对象类型，具体 API 字段另由 Handler 校验。

### 4. 异步任务可靠性

推荐写法：

> 使用 MySQL Transactional Outbox 与 Redis ZSet/Lua 构建异步 AI/文档索引任务队列，实现 pending、queued、processing、retry_wait、succeeded、dead 状态机；通过 Worker Pool、context 超时、幂等键、指数退避、lease token 和 generation fencing，使至少一次投递下的结果可追踪、可重试、可恢复。

代码证据：

- `internal/store/store.go`
- `internal/dispatcher`
- `internal/queue/redis.go`
- `internal/worker/worker.go`
- `internal/recovery/recovery.go`

测试证据：

- `internal/store/store_integration_test.go`
- `internal/queue/redis_integration_test.go`
- `internal/worker/worker_integration_test.go`
- `internal/recovery/recovery_integration_test.go`

边界：

- 不承诺 exactly-once；
- Redis Lua 只能保证 Redis 内的原子领取；
- 最终 MySQL 更新依靠状态、generation、lease token 和 lease 有效期隔离旧 Worker。

### 5. 复习任务闭环

推荐写法：

> 使用 MySQL 保存报告、复习任务和复习事件，通过 `(status, due_at)` 索引查询到期复习；支持完成、跳过、mastery 更新与再次调度，并用条件更新阻止未到期、重复点击和并发请求重复推进周期。

代码证据：

- `internal/store/store.go`
- `internal/api/operations.go`
- `internal/domain/review.go`
- `internal/migrate/sql/001_initial.sql`

测试证据：

- `internal/domain/task_test.go`
- `internal/api/api_integration_test.go`

边界：

- Redis 不保存复习到期索引；
- Redis ZSet 调度的是异步 AI/文档索引任务；
- 新报告固定从 mastery=0、一天后到期开始。

### 6. 评估、Trace 与工程化

推荐写法：

> 对模型输入输出执行 JSON Schema 校验，在 MySQL 中记录 Skill/Prompt/Schema 版本、模型、Token、延迟、Agent Step 和 Tool 调用；Real 模式拆分 DeepSeek Chat 与 OpenAI Embedding，使用 Fake Model、两类 Mock Provider、单元/集成测试及隔离 Docker Compose 验收主链、故障恢复和 RAG 引用。

代码证据：

- `internal/model`
- `internal/trace`
- `internal/migrate/sql/002_agents_documents.sql`
- `../.github/workflows/ci.yml`（仓库根目录）
- `scripts/acceptance.sh`

边界：

- Fake 模式完成端到端；
- Real 模式只完成独立 Chat/Embedding 配置校验、HTTP 超时、鉴权/限流错误、usage token 与 embedding 解析和 Mock Server 测试；
- 没有真实付费 API 压测证据；
- 没有接入通用 Trace 后端。

## 三分钟项目表达

### 20 秒定位

LearnQ 是本地单用户 AI 学习任务调度项目。我用 MySQL 保存业务事实，Redis 调度异步任务，Qdrant 保存不承载业务事实的检索索引，重点解决模型任务超时、重复执行和进程崩溃后的状态收敛。Redis 能自动修复；Qdrant 当前没有自动全量重建命令。

### 60 秒主链

学习记录、AI Task 和 Outbox 在同一 MySQL 事务提交。Dispatcher 将任务写入 Redis ready ZSet，Worker 用 Lua 原子移动到 processing，再用 MySQL lease token 获取执行权。模型成功后，报告和复习任务与 task succeeded 状态在同一事务提交，持久化成功后才 ACK Redis。

### 50 秒故障恢复

如果 Worker 崩溃，MySQL lease 过期后 Reaper 将任务推进 retry_wait 或 dead；Redis 状态丢失时 Reconciler 从 MySQL queued 任务重建 ready ZSet。旧 Worker 即使迟到，也无法通过 generation、lease token 和 lease_until 条件更新。

### 30 秒 AI/RAG

核心报告只运行 daily-review。Multi-Agent 是 Eino Compose 手动实验。文档链使用 Qdrant dense+sparse 和 RRF，DeepSeek Chat 仅根据检索证据生成带 `[S#]` 引用的回答，并用 Recall@K、NDCG 和引用覆盖率验证。

### 20 秒边界

项目不承诺 exactly-once，也没有做微服务或生产级高可用。Fake 模式完成完整验收，Real 模式只做 Mock 测试。这些边界是为了保证项目可解释和可复现。

## 面试前逐条检查

- [ ] 技术栈已改为 `Eino Compose`，没有写 ADK
- [ ] Multi-Agent 明确是手动实验，不参与核心报告
- [ ] Redis ZSet 描述为异步任务调度，不是复习索引
- [ ] 任务终态写作 `succeeded`
- [ ] Fake/Real 验收边界明确
- [ ] 每条描述都能打开对应代码
- [ ] 能运行 `make acceptance`
- [ ] 能解释至少一次、Outbox、lease fencing 和 Reconcile
