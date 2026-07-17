# LearnQ

LearnQ 是面向本地单用户的 AI 学习任务调度参考项目。它把 MySQL 作为事实来源，通过 Transactional Outbox、Redis ZSet/Lua、lease fencing 和 Worker Pool 实现至少一次投递下的结果收敛，并在同一条 Skill 工作流中支持 Fake/Real 模型、Multi-Agent、混合 RAG 与离线评估。

> 安全边界：系统没有认证，只适合本机或受信网络。Compose 只把 API 绑定到 `127.0.0.1`，不要直接暴露公网。

## 架构

```text
Web/API -> MySQL(study/task/outbox/report/review/trace)
                    |                  ^
               Dispatcher             | fenced update
                    v                  |
             Redis ready ZSet --Lua-> Worker Pool(4)
                                         |
                             Skill / deterministic agents
                               | Chat/Embedding model
                               v
                  Qdrant(dense + sparse + RRF, derived)
```

Task 状态机：

```text
pending -> queued -> processing -> succeeded
                         |
                         +-> retry_wait -> queued
                         |
                         +-> dead -> pending (人工新 generation)
```

MySQL 的 `study_records -> ai_tasks -> reports -> review_tasks` 保存业务事实；`outbox_events` 跨存储发布；`task_attempts` 和 agent/tool 表保存执行轨迹。Redis 和 Qdrant 是可从 MySQL 重建的派生状态。

## 目录

- `cmd/api`、`cmd/worker`、`cmd/migrate`：三个可执行入口。
- `internal/store|queue|worker|dispatcher`：可靠异步闭环。
- `internal/skill|model`：五个 Skill、确定性工作流与 Fake/Real adapter。
- `internal/rag`：切块、dense/sparse、Qdrant RRF、指标。
- `internal/api|web`：JSON API 与嵌入式中文页面。
- `migrations`：只执行一次且校验 checksum 的 SQL migration。
- `docs/reproduction`：从零复现材料。

## Fake 模式启动

有 Docker 时：

```bash
cp .env.example .env
docker compose up --build
curl http://127.0.0.1:8080/health/ready
```

页面位于 `http://127.0.0.1:8080/`。无 Docker 时可自行启动 MySQL/Redis/Qdrant，再运行：

```bash
GOWORK=off go run ./cmd/migrate
GOWORK=off go run ./cmd/worker
GOWORK=off go run ./cmd/api
```

## Real 模式

设置 `AI_MODE=real`、`AI_BASE_URL`、`AI_API_KEY`、`AI_CHAT_MODEL`、`AI_EMBEDDING_MODEL` 和 `EMBEDDING_DIM`。Real adapter 使用 OpenAI-compatible Chat Completions/Embeddings 接口；密钥不得提交。Fake 与 Real 共用 Skill、Schema 和工作流边界。

## API 与 Web

实现了需求列出的 `/api/v1/study-records`、Task/trace/retry、report、review、skills、documents、RAG、evaluation、weekly analytics，以及 `/health/live|ready`。成功响应使用 `data/request_id` envelope，错误返回 `code/message/request_id/details`，所有 HTTP 响应含 `X-Request-ID`。

学习记录提交需要最大 128 bytes 的 `Idempotency-Key`；同键同请求重放首次结果，不同请求返回 409。上传接受 UTF-8 Markdown/TXT/JSON，最大 5 MiB。

## RAG 与评估

文本按约 800 rune、120 rune 重叠切块并保留标题、顺序、行号与 SHA-256。稀疏向量是英文 token + 中文 unigram/bigram + FNV-1a + log-TF 的 hashed lexical retrieval，不称为 BM25。Qdrant adapter 发送 dense/sparse 各 Top 20 的 prefetch，并由 Query API 做 RRF。

离线指标代码提供 Recall@K、NDCG 和引用覆盖率，报告区分 Fake `pipeline_test` 与 Real `retrieval_benchmark`。

## 构建和测试

```bash
make fmt-check
make vet
make test
make test-integration   # 需要依赖
make acceptance        # 需要 Docker
```

任务硬超时 45s、lease 60s、最多执行 3 次，前两次失败分别等待 2s/4s。系统不承诺 exactly-once：Redis Lua 只能原子领取队列项，旧 Worker 的外部调用仍可能发生；最终 MySQL 写入同时检查状态、generation、lease token 和 lease 有效期。

故障恢复由 Outbox 重放、lease Reaper 和 Reconciler 负责。报告正文以 MySQL 为准，`data/reports` 只是可重建的原子导出副本。

验收矩阵见 [docs/ACCEPTANCE_MATRIX.md](docs/ACCEPTANCE_MATRIX.md)，复现入口见 [docs/reproduction/00-overview.md](docs/reproduction/00-overview.md)。
