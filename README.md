# LearnQ

[![CI](https://github.com/WoAiXueXiHa/LeranQ/actions/workflows/ci.yml/badge.svg)](https://github.com/WoAiXueXiHa/LeranQ/actions/workflows/ci.yml)

LearnQ 是一个面向个人学习复盘的本地 AI 应用。用户提交一次学习记录后，系统会异步生成结构化报告和复习任务；也可以上传个人文档，通过混合检索获得带来源引用的回答。

项目重点不在堆叠页面或模型调用，而在于把一次 AI 请求扩展成可恢复、可追踪、可验证的完整后端链路，适合作为 Go 后端和 ai 工程化的学习项目。

## 核心功能

- 学习记录：记录主题、摘要、学习时长和模块内容，支持幂等提交与重复记录识别。
- AI 报告：通过异步任务生成结构化学习报告，并自动建立后续复习任务。
- 延迟复习：查询到期和即将到期的任务，提交掌握程度或将复习延后一天。
- 文档与 RAG：异步切分和索引 Markdown、TXT、JSON 文档，支持单文档重建、批量重建与 Qdrant 蓝绿切换，使用 dense、sparse 与 RRF 融合检索返回可核验引用。
- Skill 工作台：运行版本化 Skill 和 Eino Compose Multi-Agent 实验，保存 Agent、Tool、耗时和 token 轨迹。
- 运行状态：展示任务时间线、失败原因、报告、引用和依赖健康状态，并通过 /metrics 暴露队列、Outbox、租约、恢复、模型耗时和 token 指标。

## 系统架构

```text
Browser
   |
   v
Gin API + Embedded Web
   |
   +---- MySQL ----------------------------------------+
   |     业务事实 / 任务 / Outbox / 报告 / Trace        |
   |                                                   |
   +--> Outbox Dispatcher --> Redis ready queue        |
                                 |                     |
                                 v                     |
                          Worker Pool                  |
                     Skill / document_index            |
                                 |                     |
                                 +--> fenced update ---+
                                 |
                                 +--> Qdrant
                                      dense + sparse + RRF
```

### 学习报告链路

```text
创建学习记录
  -> MySQL 事务写入 record + task + outbox
  -> Dispatcher 投递 Redis
  -> Worker 原子领取并建立 lease
  -> daily-review Skill 生成报告
  -> fenced update 写入 report + review_task
  -> ACK 队列任务
```

### 文档问答链路

```text
上传文档
  -> 创建 document_index 异步任务
  -> 文档切块并保存元数据
  -> Qdrant 写入 dense/sparse 向量
  -> RAG 查询分别召回并通过 RRF 融合
  -> 模型基于证据生成带来源标记的回答
```

## 设计亮点

### MySQL 作为事实源

学习记录、任务状态、报告、复习、文档和执行轨迹都以 MySQL 为最终事实。Redis 负责可恢复的任务调度，Qdrant 保存可重建的检索索引，避免把业务正确性绑定到缓存状态。

### Transactional Outbox

业务数据、异步任务和 Outbox 事件在同一个数据库事务中提交，缩小“数据库已经成功、队列投递却失败”的任务丢失窗口。Dispatcher 可以重复投递，消费端依靠状态、generation 和条件更新收敛。

### Lease 与 Fencing

Redis Lua 脚本原子完成任务领取，lease token 隔离过期 Worker，generation 隔离人工重试前后的任务代次。Worker 只有在 MySQL 成功持久化结果后才清理 Redis processing 状态。

### Reaper 与 Reconciler

Reaper 从 MySQL 分批扫描过期任务并推进重试或终止；Reconciler 使用持久游标分页补回 Redis 中缺失的任务并清理幽灵 processing 项，避免数据增长后每轮全量扫描，覆盖进程崩溃和瞬时依赖故障。

### 可核验的 AI 与 RAG

系统保存 Skill、Agent、Tool 和任务尝试轨迹。RAG 同时使用语义向量与词法稀疏向量，回答必须引用本次召回到的来源；离线评估提供 Recall@K、NDCG 和引用覆盖率。

### 可重建索引与轻量可观测性

每个文档保存 index_version，单文档重建会创建新的 document_index 任务，旧任务和 attempt 保留用于审计。普通批量重建复用同一可靠队列；Embedding 升级还可先在新 Qdrant collection 中重算当前持久化 chunks，全部成功后一次性切换 alias，失败时旧索引继续服务。

/metrics 直接输出 Prometheus 文本，覆盖任务状态、队列深度、未投递 Outbox、最老等待时间、过期租约、恢复动作、fencing 拒绝、HTTP/RAG/模型错误、模型耗时和 token。它是本地项目的轻量观测面，不包含告警平台或分布式 tracing 基础设施。

## 技术栈

| 分类 | 技术 |
|---|---|
| 后端 | Go 1.25、Gin、GORM |
| 数据 | MySQL 8.4、Redis 7.2、Qdrant 1.18 |
| AI 编排 | CloudWeGo Eino Compose、版本化 Skill Registry |
| 前端 | 原生 HTML、CSS、JavaScript，随 Go 服务嵌入 |
| 工程化 | Docker Compose、Makefile、GitHub Actions |
| 模型适配 | 内置 Fake Adapter、DeepSeek Chat、Ollama Qwen3 Embedding |

## 仓库结构

```text
.
├── .github/workflows/       # 仓库级 CI
├── cmd/                     # migrate、api、worker、reindex 入口
├── internal/
│   ├── api/                 # HTTP API 与读模型
│   ├── store/               # MySQL 事务与任务状态
│   ├── queue/               # Redis 队列与 Lua
│   ├── worker/              # 异步任务执行
│   ├── recovery/            # Reaper 与 Reconciler
│   ├── skill/               # Skill 与 Eino 工作流
│   ├── indexer/             # 文档切块和索引
│   ├── rag/                 # 混合检索与评估
│   └── web/                 # 嵌入式前端
├── scripts/                 # 验收和手工测试
├── RUNBOOK.md               # 启动、配置、接口与排障手册
├── compose.yaml
├── go.mod
└── README.md
```

## 项目边界

- 这是单用户、本地优先的学习项目，不包含认证、RBAC、多租户或公网部署能力。
- Compose 默认只将 API 暴露到 `127.0.0.1`，不应直接开放到公网。
- 异步任务采用至少一次投递和最终状态收敛，不宣称 exactly-once。
- Fake 模式可离线演示完整业务链路；真实模型密钥只从环境变量读取。
- 保持 API、Worker 和嵌入式前端的清晰边界，不为展示复杂度而拆分微服务。

启动、配置、API 和验收操作见 [RUNBOOK.md](RUNBOOK.md)。
