# LearnQ 应用手册

本手册只说明如何启动、配置、验证和排查 LearnQ。项目定位、架构和设计取舍见仓库根目录的 [README](../README.md)。

> LearnQ 没有用户认证。Compose 默认仅监听 `127.0.0.1`，请勿直接暴露到公网。

## 环境要求

- Docker Engine 与 Docker Compose v2
- `curl`、`jq` 和 `make`
- 仅在本地直接运行 Go 进程或执行测试时需要 Go 1.25

## 启动与停止

```bash
cp .env.example .env
docker compose up --build -d
curl -fsS http://127.0.0.1:8080/health/ready | jq
```

浏览器访问 <http://127.0.0.1:8080/>。就绪响应中的 `mysql`、`redis`、`qdrant` 和 `worker` 应全部为 `ok`。

查看状态和日志：

```bash
docker compose ps
docker compose logs -f api worker
```

停止服务并保留数据卷：

```bash
make down
```

仅在确认不需要本地数据时清空数据卷：

```bash
make reset-data
```

如果 8080 端口被占用，可以修改 `.env`：

```dotenv
LEARNQ_HTTP_PORT=18080
```

随后使用对应端口访问页面和健康检查。

## 模型配置

默认配置为 `AI_MODE=fake`，不会调用外部模型，也不需要 API Key。该模式能够演示学习报告、复习、文档索引、RAG、Skill 和 Trace 的完整链路，推荐用于本地开发和自动验收。

真实模型模式需要同时配置 Chat 与 Embedding。推荐使用 DeepSeek Chat，并通过本机 Ollama
运行 `qwen3-embedding:0.6b`，Embedding 不产生云端 API 调用费用。

先在宿主机准备 Ollama：

```bash
docker run -d \
  --name ollama \
  --restart unless-stopped \
  -v ollama-models:/root/.ollama \
  -p 11434:11434 \
  ollama/ollama

docker exec ollama ollama pull qwen3-embedding:0.6b
```

然后修改被 Git 忽略的 `.env`：

```dotenv
AI_MODE=real
AI_CHAT_BASE_URL=https://api.deepseek.com
AI_CHAT_API_KEY=replace-with-your-deepseek-key
AI_CHAT_MODEL=deepseek-v4-pro
AI_EMBEDDING_BASE_URL=http://host.docker.internal:11434/v1
AI_EMBEDDING_API_KEY=ollama
AI_EMBEDDING_MODEL=qwen3-embedding:0.6b
EMBEDDING_DIM=64
```

`AI_EMBEDDING_API_KEY=ollama` 只是兼容接口所需的占位值，不是真实密钥。
Compose 通过 `host.docker.internal` 访问宿主机的 `11434` 端口；直接运行 Go 进程时，
将 `AI_EMBEDDING_BASE_URL` 改为 `http://127.0.0.1:11434/v1`。

| 变量 | 默认值 | 用途 |
|---|---|---|
| `HTTP_ADDR` | `127.0.0.1:8080` | 本地进程监听地址；Compose 内部会覆盖为 `0.0.0.0:8080` |
| `MYSQL_DSN` | 见 `.env.example` | MySQL 连接串 |
| `REDIS_ADDR` | `127.0.0.1:6379` | Redis 地址 |
| `QDRANT_URL` | `http://127.0.0.1:6333` | Qdrant HTTP 地址 |
| `AI_MODE` | `fake` | `fake` 或 `real` |
| `AI_EMBEDDING_BASE_URL` | `http://host.docker.internal:11434/v1` | Compose 访问宿主机 Ollama 的 OpenAI 兼容地址 |
| `AI_EMBEDDING_MODEL` | `qwen3-embedding:0.6b` | 本地 Embedding 模型 |
| `REPORT_DIR` | `data/reports` | Markdown 报告导出目录 |
| `LEARNQ_HTTP_PORT` | `8080` | Compose 对宿主机暴露的端口 |

不要把真实密钥写入 `.env.example` 或其他受 Git 跟踪的文件；本地密钥只保存在 `.env`。
修改 `EMBEDDING_DIM` 后，必须确保它与现有 Qdrant collection 一致；开发环境中如需重建索引，
应先确认数据可以清除。

## 核心 API

成功响应使用 `data/request_id` envelope，错误响应包含 `code`、`message`、`request_id` 和 `details`；所有响应都带有 `X-Request-ID`。

| 方法 | 路径 | 用途 |
|---|---|---|
| `GET` | `/health/live` | 进程存活检查 |
| `GET` | `/health/ready` | MySQL、Redis、Qdrant、Worker 就绪检查 |
| `POST` | `/api/v1/study-records` | 创建学习记录和异步报告任务 |
| `GET` | `/api/v1/study-records` | 查询最近学习记录 |
| `GET` | `/api/v1/tasks/:id/detail` | 查询任务、报告、复习和 Trace |
| `POST` | `/api/v1/tasks/:id/retry` | 重试已经终止的任务 |
| `GET` | `/api/v1/review-tasks?scope=due` | 查询复习任务；空结果为 `[]` |
| `POST` | `/api/v1/review-tasks/:id/complete` | 完成复习并提交掌握程度 |
| `POST` | `/api/v1/review-tasks/:id/skip` | 延后一天 |
| `GET` | `/api/v1/skills` | 查询 Skill Registry |
| `POST` | `/api/v1/skills/:name/runs` | 手动运行 Skill |
| `POST` | `/api/v1/documents` | 上传 UTF-8 Markdown、TXT 或 JSON 文档 |
| `GET` | `/api/v1/documents` | 查询文档及索引状态 |
| `DELETE` | `/api/v1/documents/:id` | 删除文档和向量索引 |
| `POST` | `/api/v1/rag/query` | 基于已就绪文档进行 RAG 查询 |
| `POST` | `/api/v1/evaluations/rag` | 执行 RAG 离线评估 |
| `GET` | `/api/v1/analytics/weekly` | 查询周统计 |

创建学习记录：

```bash
curl -fsS -X POST http://127.0.0.1:8080/api/v1/study-records \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: readme-demo-001' \
  -d '{
    "title":"Redis 任务队列",
    "summary":"整理 ZSet、Lua 原子领取和 lease fencing",
    "duration_minutes":60,
    "modules":[{"category":"backend","content":"完成任务领取与恢复测试"}]
  }' | jq
```

查询任务详情：

```bash
curl -fsS http://127.0.0.1:8080/api/v1/tasks/1/detail | jq
```

上传并查询文档：

```bash
curl -fsS -X POST http://127.0.0.1:8080/api/v1/documents \
  -F 'file=@README.md;type=text/markdown' | jq

curl -fsS -X POST http://127.0.0.1:8080/api/v1/rag/query \
  -H 'Content-Type: application/json' \
  -d '{"question":"LearnQ 的任务如何恢复？","top_k":5}' | jq
```

## 验证

代码质量与单元测试：

```bash
make fmt-check
make vet
make test
GOWORK=off go test -race ./...
```

带 MySQL 和 Redis 的集成测试：

```bash
make test-integration
```

执行前应提供测试所需的 `MYSQL_DSN`、`REDIS_ADDR` 和 `QDRANT_URL`。测试会使用独立数据并在结束后清理。

完整验收：

```bash
make acceptance
```

验收脚本会创建独立的 Compose 项目、端口和数据卷，覆盖：

- 服务构建与就绪检查
- 学习记录、异步报告、复习计划与幂等
- Skill、Multi-Agent 和执行轨迹
- 文档索引、RAG 引用和离线评估
- 周统计与 Worker 停启恢复

脚本退出时会清理自己的容器和数据卷，不影响默认 LearnQ 环境。自动测试始终使用 Fake 或 Mock 模型，不会产生付费 API 调用。

## 常见问题

| 现象 | 检查与处理 |
|---|---|
| `/health/ready` 返回 503 | 查看响应中的 `dependencies`，再检查对应容器和 `api/worker` 日志 |
| 页面一直显示任务处理中 | 确认 `worker` 为 `ok`，查看任务 `last_error` 和 Worker 日志 |
| 8080 端口被占用 | 在 `.env` 设置新的 `LEARNQ_HTTP_PORT` 后重启 Compose |
| Real 模式启动失败 | 检查两套 Base URL、API Key、模型名和 `EMBEDDING_DIM` 是否完整 |
| Ollama Embedding 调用失败 | 检查 `ollama` 容器、宿主机 `11434` 端口以及模型是否已经拉取 |
| Qdrant 提示维度不一致 | 恢复原维度；仅在可丢弃本地索引时才清理并重建数据卷 |
| 文档无法索引 | 确认文件为 UTF-8 的 Markdown、TXT 或 JSON，且不超过 5 MiB |
| RAG 按钮不可用 | 至少等待一个文档状态变为“可查询” |
| 需要重新开始演示 | 先确认本地数据无保留价值，再运行 `make reset-data` 并重新启动 |

## 本地直接运行

仅在已经自行准备 MySQL、Redis 和 Qdrant 时使用：

```bash
GOWORK=off go run ./cmd/migrate
GOWORK=off go run ./cmd/worker
GOWORK=off go run ./cmd/api
```

三个进程需要使用同一套环境变量；先执行迁移，再启动 Worker 和 API。
