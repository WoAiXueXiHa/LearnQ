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

真实模型模式需要同时配置 Chat、Embedding 与 Vision。推荐使用 DeepSeek Chat，并通过本机
Ollama 运行 `qwen3-embedding:0.6b` 和 `qwen3-vl:4b`；Embedding 和图片像素都只发往本机
Ollama。RAG 最终回答会把命中的知识库片段发送给 Chat 服务商，上传资料前应确认其隐私边界。

先在宿主机准备 Ollama：

```bash
docker run -d \
  --name ollama \
  --restart unless-stopped \
  -v ollama-models:/root/.ollama \
  -p 11434:11434 \
  ollama/ollama

docker exec ollama ollama pull qwen3-embedding:0.6b
docker exec ollama ollama pull qwen3-vl:4b
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
AI_VISION_PROVIDER=ollama
AI_VISION_BASE_URL=http://host.docker.internal:11434/v1
AI_VISION_API_KEY=ollama
AI_VISION_MODEL=qwen3-vl:4b
AI_VISION_CONTEXT_LENGTH=8192
TASK_TIMEOUT=120s
LEASE_DURATION=150s
```

`AI_EMBEDDING_API_KEY=ollama` 和 `AI_VISION_API_KEY=ollama` 只是本地兼容配置，不是真实密钥。
Compose 通过 `host.docker.internal` 访问宿主机的 `11434` 端口；直接运行 Go 进程时，
将两项 Ollama Base URL 改为 `http://127.0.0.1:11434/v1`。Ollama Vision 使用原生
`/api/chat`，会自动移除 Base URL 末尾的 `/v1`，请求关闭思考并按 JSON Schema 输出；
同时兼容 Qwen3-VL 将结构化 JSON 放入 `thinking` 字段的行为。

| 变量 | 默认值 | 用途 |
|---|---|---|
| `HTTP_ADDR` | `127.0.0.1:8080` | 本地进程监听地址；Compose 内部会覆盖为 `0.0.0.0:8080` |
| `MYSQL_DSN` | 见 `.env.example` | MySQL 连接串 |
| `REDIS_ADDR` | `127.0.0.1:6379` | Redis 地址 |
| `QDRANT_URL` | `http://127.0.0.1:6333` | Qdrant HTTP 地址 |
| `AI_MODE` | `fake` | `fake` 或 `real` |
| `AI_CHAT_BASE_URL` | `https://api.deepseek.com` | 报告、Skill 和 RAG 最终回答使用的 Chat 服务 |
| `AI_EMBEDDING_BASE_URL` | `http://host.docker.internal:11434/v1` | Compose 访问宿主机 Ollama 的 OpenAI 兼容地址 |
| `AI_EMBEDDING_MODEL` | `qwen3-embedding:0.6b` | 本地 Embedding 模型 |
| `RAG_COLLECTION` | 按模式和 Embedding 模型生成 | 隔离 Fake 与 Real 向量，通常无需手动设置 |
| `AI_VISION_PROVIDER` | `ollama` | `ollama` 使用原生接口；`openai` 使用 OpenAI 兼容接口 |
| `AI_VISION_BASE_URL` | `http://host.docker.internal:11434/v1` | 本地视觉模型地址 |
| `AI_VISION_MODEL` | `qwen3-vl:4b` | 本地视觉模型 |
| `AI_VISION_CONTEXT_LENGTH` | `8192` | Ollama Vision 上下文；长截图不应低于该值 |
| `TASK_TIMEOUT` | `45s` | 单次任务执行超时；Real 视觉模式建议按机器性能调大 |
| `LEASE_DURATION` | `60s` | Worker 租约，必须严格大于 `TASK_TIMEOUT` |
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
| `POST` | `/api/v1/images` | 上传 PNG/JPEG 并创建异步视觉理解任务 |
| `GET` | `/api/v1/images` | 查询图片及分析状态 |
| `GET` | `/api/v1/images/:id` | 查询图片、结构化描述和衍生文档 |
| `GET` | `/api/v1/images/:id/content` | 获取原始图片内容 |
| `POST` | `/api/v1/images/:id/retry` | 重新分析失败的图片 |
| `POST` | `/api/v1/images/:id/index` | 将已完成描述幂等地加入知识库 |
| `DELETE` | `/api/v1/images/:id` | 删除图片及其衍生文档/向量 |
| `POST` | `/api/v1/rag/query` | 基于已就绪文档进行 RAG 查询 |
| `POST` | `/api/v1/evaluations/rag` | 使用包含真实 chunk ID 的 JSONL 执行 RAG 离线评估 |
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

上传图片、等待描述完成并加入知识库：

```bash
response=$(curl -fsS -X POST http://127.0.0.1:8080/api/v1/images \
  -F 'file=@/absolute/path/to/image.png;type=image/png' \
  -F 'prompt=提取文字、关键知识点并面向后端学习解释')
image_id=$(printf '%s' "$response" | jq -r '.data.id')

curl -fsS "http://127.0.0.1:8080/api/v1/images/$image_id" | jq
curl -fsS -X POST "http://127.0.0.1:8080/api/v1/images/$image_id/index" | jq
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

执行前必须显式提供只用于测试的 `LEARNQ_TEST_MYSQL_DSN` 和 `LEARNQ_TEST_REDIS_ADDR`；
未提供时相关测试会安全跳过。不要把默认开发环境的 MySQL 或 Redis 地址填入这些变量。

完整验收：

```bash
make acceptance
```

验收脚本会创建独立的 Compose 项目、端口和数据卷，覆盖：

- 服务构建与就绪检查
- 学习记录、异步报告、复习计划与幂等
- Skill、Multi-Agent 和执行轨迹
- 图片上传、异步视觉描述、状态查询和幂等加入知识库
- 文档索引、RAG 引用和离线评估
- 周统计与 Worker 停启恢复

脚本退出时会清理自己的容器和数据卷，不影响默认 LearnQ 环境。自动测试始终使用 Fake 或 Mock 模型，不会产生付费 API 调用。

## 常见问题

| 现象 | 检查与处理 |
|---|---|
| `/health/ready` 返回 503 | 查看响应中的 `dependencies`，再检查对应容器和 `api/worker` 日志 |
| 页面一直显示任务处理中 | 确认 `worker` 为 `ok`，查看任务 `last_error` 和 Worker 日志 |
| 8080 端口被占用 | 在 `.env` 设置新的 `LEARNQ_HTTP_PORT` 后重启 Compose |
| Real 模式启动失败 | 检查 Chat、Embedding、Vision 三套配置及 `EMBEDDING_DIM` 是否完整 |
| Ollama Embedding 调用失败 | 检查 `ollama` 容器、宿主机 `11434` 端口以及模型是否已经拉取 |
| 图片长期处理中 | 检查 `qwen3-vl:4b` 是否驻留 GPU、`TASK_TIMEOUT` 是否合理，并确认 `LEASE_DURATION > TASK_TIMEOUT` |
| 图片提示 context exhausted | 保持 `AI_VISION_PROVIDER=ollama`，并将 `AI_VISION_CONTEXT_LENGTH` 设为至少 `8192` |
| 图片分析完成但知识库没有内容 | 图片默认不会自动入库；在页面点击“加入知识库”或调用 `/images/:id/index` |
| Qdrant 提示维度不一致 | 恢复原维度；仅在可丢弃本地索引时才清理并重建数据卷 |
| Qdrant 数据卷丢失或集合为空 | `/health/ready` 只代表服务可用；当前版本需要重新上传文档，不会自动重建历史向量 |
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
