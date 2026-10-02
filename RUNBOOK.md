# LearnQ 应用手册

本手册只说明如何启动、配置、验证和排查 LearnQ。项目定位、架构和设计取舍见仓库根目录的 [README](README.md)。

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

真实模型模式需要同时配置 Chat、Embedding 与 Vision，默认目标配置全部使用外部服务：Chat 与
Vision 走 DeepSeek，Embedding 走 Cloudflare Workers AI 的 `@cf/qwen/qwen3-embedding-0.6b`
（1024 维），服务器不下载本地推理模型。该配置下，Markdown 正文片段与问题会发送到
Cloudflare，生成题目/反馈所需的证据与外链图片会发送到 DeepSeek；RAG 最终回答会把命中的
知识库片段发送给 Chat 服务商，上传资料前应确认其隐私边界。

先准备两组凭据：

- DeepSeek：在控制台创建 API Key，填入 `AI_CHAT_API_KEY`；`AI_VISION_API_KEY` 可填同一把。
- Cloudflare：Account ID 替换 `AI_EMBEDDING_BASE_URL` 里的 `REPLACE_WITH_CF_ACCOUNT_ID`，
  另建一个带 Workers AI 权限的 API Token 填入 `AI_EMBEDDING_API_KEY`。

然后修改被 Git 忽略的 `.env`：

```dotenv
AI_MODE=real
AI_CHAT_BASE_URL=https://api.deepseek.com
AI_CHAT_API_KEY=<DeepSeek API Key>
AI_CHAT_MODEL=deepseek-flash
AI_EMBEDDING_BASE_URL=https://api.cloudflare.com/client/v4/accounts/<ACCOUNT_ID>/ai/v1
AI_EMBEDDING_API_KEY=<Cloudflare API Token>
AI_EMBEDDING_MODEL=@cf/qwen/qwen3-embedding-0.6b
EMBEDDING_DIM=1024
AI_VISION_PROVIDER=openai
AI_VISION_BASE_URL=https://api.deepseek.com
AI_VISION_API_KEY=<与 AI_CHAT_API_KEY 相同的 DeepSeek Key>
AI_VISION_MODEL=deepseek-flash
TASK_TIMEOUT=120s
LEASE_DURATION=150s
```

`AI_CHAT_API_KEY` 与 `AI_VISION_API_KEY` 可以指向同一把 DeepSeek 凭据，但两项分别校验，
只填一处会在启动时报出缺少的另一项。Embedding 走 OpenAI 兼容的 `/embeddings`，Base URL
需以 `/ai/v1` 结尾。`AI_VISION_CONTEXT_LENGTH` 只对 Ollama 路径有效，OpenAI 兼容路径不使用。

仍保留本地 Ollama 作为备选：把两项 Base URL 指向 `http://host.docker.internal:11434/v1`
（直接运行 Go 进程时用 `http://127.0.0.1:11434/v1`），`AI_VISION_PROVIDER` 设回 `ollama`，
`AI_EMBEDDING_MODEL`/`AI_VISION_MODEL` 改为 `qwen3-embedding:0.6b`/`qwen3-vl:4b`。该路径需要
先在宿主机准备 Ollama（`ollama/ollama` 镜像，拉取上述两个模型）；Ollama Vision 使用原生
`/api/chat`，会自动移除 Base URL 末尾的 `/v1`，请求关闭思考并按 JSON Schema 输出，
同时兼容 Qwen3-VL 将结构化 JSON 放入 `thinking` 字段的行为。

| 变量 | 默认值 | 用途 |
|---|---|---|
| `HTTP_ADDR` | `127.0.0.1:8080` | 本地进程监听地址；Compose 内部会覆盖为 `0.0.0.0:8080` |
| `MYSQL_DSN` | 见 `.env.example` | MySQL 连接串 |
| `REDIS_ADDR` | `127.0.0.1:6379` | Redis 地址 |
| `QDRANT_URL` | `http://127.0.0.1:6333` | Qdrant HTTP 地址 |
| `AI_MODE` | `fake` | `fake` 或 `real` |
| `AI_CHAT_BASE_URL` | `https://api.deepseek.com` | 报告、Skill 和 RAG 最终回答使用的 Chat 服务 |
| `AI_CHAT_MODEL` | `deepseek-flash` | Chat 模型名 |
| `AI_EMBEDDING_BASE_URL` | `https://api.cloudflare.com/client/v4/accounts/<ACCOUNT_ID>/ai/v1` | Cloudflare Workers AI 的 OpenAI 兼容地址，需替换 `<ACCOUNT_ID>` |
| `AI_EMBEDDING_MODEL` | `@cf/qwen/qwen3-embedding-0.6b` | Embedding 模型（1024 维） |
| `RAG_COLLECTION` | 按模式和 Embedding 模型生成 | 隔离 Fake 与 Real 向量，通常无需手动设置 |
| `AI_VISION_PROVIDER` | `openai` | `ollama` 使用原生接口；`openai` 使用 OpenAI 兼容接口 |
| `AI_VISION_BASE_URL` | `https://api.deepseek.com` | 视觉服务地址（OpenAI 兼容） |
| `AI_VISION_MODEL` | `deepseek-flash` | 视觉模型 |
| `AI_VISION_CONTEXT_LENGTH` | `8192` | 仅 Ollama Vision 使用的上下文长度；长截图不应低于该值 |
| `TASK_TIMEOUT` | `45s` | 单次任务执行超时；Real 视觉模式建议按机器性能调大 |
| `LEASE_DURATION` | `60s` | Worker 租约，必须严格大于 `TASK_TIMEOUT` |
| `SHUTDOWN_GRACE` | `70s` | Worker 停机等待时间，必须大于 0，通常应覆盖租约时长 |
| `REPORT_DIR` | `data/reports` | Markdown 报告导出目录 |
| `LEARNQ_HTTP_PORT` | `8080` | Compose 对宿主机暴露的端口 |

不要把真实密钥写入 `.env.example` 或其他受 Git 跟踪的文件；本地密钥只保存在 `.env`。
`@cf/qwen/qwen3-embedding-0.6b` 返回 1024 维向量，`EMBEDDING_DIM` 必须与其实际输出和现有
Qdrant collection 一致；更换 Embedding 模型会改变 `RAG_COLLECTION` 名，旧向量不再被检索到，
需要重建索引。开发环境中如需重建索引，应先确认数据可以清除。

## 核心 API

成功响应使用 `data/request_id` envelope，错误响应包含 `code`、`message`、`request_id` 和 `details`；所有响应都带有 `X-Request-ID`。

| 方法 | 路径 | 用途 |
|---|---|---|
| `GET` | `/health/live` | 进程存活检查 |
| `GET` | `/health/ready` | MySQL、Redis、Qdrant、Worker 就绪检查 |
| `GET` | `/metrics` | Prometheus 文本指标 |
| `POST` | `/api/v1/study-records` | 创建学习记录和异步报告任务 |
| `GET` | `/api/v1/study-records` | 查询最近学习记录 |
| `GET` | `/api/v1/tasks/:id/detail` | 查询任务、报告、复习和 Trace |
| `POST` | `/api/v1/tasks/:id/retry` | 重试已经终止的任务 |
| `GET` | `/api/v1/review-tasks?scope=due` | 查询复习任务；空结果为 `[]` |
| `POST` | `/api/v1/review-tasks/:id/complete` | 完成复习并提交掌握程度 |
| `POST` | `/api/v1/review-tasks/:id/skip` | 延后一天 |
| `GET` | `/api/v1/skills` | 查询 Skill Registry |
| `POST` | `/api/v1/skills/:name/runs` | 手动运行 Skill |
| `POST` | `/api/v1/agent/runs` | 创建一次受控 Agent 执行 |
| `GET` | `/api/v1/agent-runs?limit=10` | 查询最近 Agent Run |
| `GET` | `/api/v1/agent-runs/:id` | 查询 Agent Run、步骤、工具调用和展示数据 |
| `POST` | `/api/v1/documents` | 上传 UTF-8 Markdown、TXT 或 JSON 文档 |
| `GET` | `/api/v1/documents` | 查询文档及索引状态与 index_version |
| `POST` | `/api/v1/documents/:id/reindex` | 为文档创建新的可靠重建任务 |
| `DELETE` | `/api/v1/documents/:id` | 删除文档和向量索引 |
| `POST` | `/api/v1/images` | 上传 PNG/JPEG 并创建异步视觉理解任务 |
| `GET` | `/api/v1/images` | 查询图片及分析状态 |
| `GET` | `/api/v1/images/:id` | 查询图片、结构化描述和衍生文档 |
| `GET` | `/api/v1/images/:id/content` | 获取原始图片内容 |
| `POST` | `/api/v1/images/:id/retry` | 重新分析失败的图片 |
| `POST` | `/api/v1/images/:id/index` | 将已完成描述幂等地加入知识库 |
| `DELETE` | `/api/v1/images/:id` | 删除图片及其衍生文档/向量 |
| `POST` | `/api/v1/rag/query` | 基于已就绪文档进行 RAG 查询 |
| `POST` | `/api/v1/evaluations/rag` | 使用真实 chunk ID 或可唯一解析的 `citation_text` JSONL 执行 RAG 离线评估 |
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

运行 RAG 离线评估：

```bash
curl -fsS -X POST http://127.0.0.1:8080/api/v1/documents \
  -F 'file=@README.md;type=text/markdown' | jq
curl -fsS -X POST http://127.0.0.1:8080/api/v1/documents \
  -F 'file=@RUNBOOK.md;type=text/markdown' | jq

# 等待文档状态变为 ready 后执行。data/eval/rag.jsonl 使用 citation_text，
# 服务端会解析为当前 ready chunk；命中 0 个或多个 chunk 时评测会拒绝执行。
curl -fsS -X POST http://127.0.0.1:8080/api/v1/evaluations/rag \
  --data-binary @data/eval/rag.jsonl | jq
```

报告中的 Markdown 表按 `dense-only`、`sparse-only` 和 `hybrid-rrf` 展示 Recall@5、NDCG 与检索覆盖率。一次成功评估会写入 `rag_evaluations`，可通过 `GET /api/v1/evaluations/rag/:id/report.md` 取回同一张结果表。

创建 Agent Run 并查看完整执行结果：

```bash
curl -fsS -X POST http://127.0.0.1:8080/api/v1/agent/runs \
  -H 'Content-Type: application/json' \
  -d '{"message":"帮我说明 LearnQ 的 RAG 链路","mode":"auto","allow_actions":false,"context":{}}' | jq
```

响应中的 `plan`、`tool_calls`、`evidence`、`answer` 和 `self_check` 对应页面上的五段式 Trace。打开浏览器中的 Agent 工作台可以直接查看任务判断、计划步骤、受控工具调用、证据卡片、最终回答和自检结论；原始 JSON 只在需要排查时展开。

知识问答没有有效证据时会返回依据不足，不允许模型脱离知识库自由补充。涉及创建复习任务的请求必须同时满足 `allow_actions=true` 和有效 `report_id`，否则只展示建议，不产生写操作。

建议使用以下三条路径验收 Agent 展示：

1. 输入知识库中已有主题，确认出现检索工具、`S1` 引用和最终回答。
2. 输入知识库中不存在的主题，确认显示依据不足，并在自检区域标记证据不足。
3. 请求创建复习任务但关闭动作权限，确认工具状态为已拦截且没有新增复习任务。

任务详情页中的旧版学习报告也会展示同样的 Trace 结构。若页面仍显示旧的单行 JSON，先执行 `docker compose up --build -d api`，再刷新浏览器缓存。

上传图片、等待描述完成并加入知识库：

```bash
response=$(curl -fsS -X POST http://127.0.0.1:8080/api/v1/images \
  -F 'file=@/absolute/path/to/image.png;type=image/png' \
  -F 'prompt=提取文字、关键知识点并面向后端学习解释')
image_id=$(printf '%s' "$response" | jq -r '.data.id')

curl -fsS "http://127.0.0.1:8080/api/v1/images/$image_id" | jq
curl -fsS -X POST "http://127.0.0.1:8080/api/v1/images/$image_id/index" | jq
```

## 索引重建与指标

单文档重建会创建新的异步任务，旧任务和执行尝试仍可查询：

~~~bash
curl -fsS -X POST http://127.0.0.1:8080/api/v1/documents/1/reindex | jq
~~~

本地直接运行进程、并已配置同一套 MySQL 环境变量时，可批量把 ready/failed 文档送入可靠队列：

~~~bash
GOWORK=off go run ./cmd/reindex --all --limit 1000
~~~

仅升级 Embedding、持久化 chunks 未变化时，可进行蓝绿重建。运行中的 API 和 Worker 必须把 RAG_COLLECTION 配置为稳定 alias（例如 learnq_live），shadow collection 必须是新的物理集合名：

~~~bash
GOWORK=off go run ./cmd/reindex --all --limit 1000 \
  --shadow-collection learnq_chunks_v2 --alias learnq_live
~~~

命令只选择 ready 文档，复用 MySQL 已持久化 chunks 写入新 collection；任一文档失败都不会切换 alias。全部成功后，Qdrant 在一个 alias 更新请求中删除旧指向并创建新指向。若切块算法或 chunk ID 规则变化，请使用普通单文档/批量重建，不要使用 shadow 模式。

指标可直接抓取：

~~~bash
curl -fsS http://127.0.0.1:8080/metrics
~~~

重点关注 learnq_outbox_oldest_wait_seconds、learnq_expired_leases、learnq_queue_ready、learnq_fencing_rejections_total、learnq_reconciler_errors_total、learnq_model_chat_latency_sum_ms/count 和模型 token 累计值。计数器保存在 Redis，Redis 丢失不会影响业务事实，只会重新累计指标。

## 验证

代码质量与单元测试：

```bash
make fmt-check
make vet
make test
GOWORK=off go test -race ./...
```

真实模式下 Multi-Agent 会发起多次模型调用，手工验收建议保留默认的 120 秒请求上限；如需覆盖：

```bash
CURL_MAX_TIME=180 bash scripts/manual-test.sh
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
- 文档索引、单文档重建、index_version、RAG 引用、运行指标和离线评估
- 周统计与 Worker 停启恢复

脚本退出时会清理自己的容器和数据卷，不影响默认 LearnQ 环境。自动测试始终使用 Fake 或 Mock 模型，不会产生付费 API 调用。

## 故障恢复演示

以下实验会修改队列、任务状态或停止 Worker，建议在临时 Compose 项目中执行。每个实验结束后都可以用 `/api/v1/tasks/:id/detail`、`/metrics` 或 MySQL 查询核对。

### 模型临时失败重试

自动证据：

```bash
GOWORK=off go test ./internal/model -run TestFakeFailureInjection
LEARNQ_TEST_MYSQL_DSN='learnq:learnq@tcp(127.0.0.1:3306)/learnq?parseTime=true&charset=utf8mb4&multiStatements=true' \
  GOWORK=off go test -tags=integration ./internal/store -run TestThreeAttemptsUseRetryWaitThenDead
```

预期：Fake 模型能注入 `temporary model error`；Store 在临时失败后按退避把任务推进 `retry_wait` 或最终 `dead`，不会把失败报告标记为成功。

### Redis 队列丢失后 Reconciler 补回

```bash
body='{"title":"reconcile demo","summary":"queue loss","duration_minutes":20,"modules":[{"category":"backend","content":"outbox reconciler"}]}'
task_id=$(curl -fsS -X POST http://127.0.0.1:8080/api/v1/study-records \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: reconcile-demo' \
  -d "$body" | jq -r '.data.task_id')

docker compose exec mysql mysql -ulearnq -plearnq learnq \
  -e "UPDATE ai_tasks SET status='queued', available_at=DATE_ADD(UTC_TIMESTAMP(6), INTERVAL 10 MINUTE) WHERE id=$task_id"
docker compose exec redis redis-cli ZREM learnq:tasks:ready "$task_id"

# Worker 内置 Reconciler 周期会补回；也可重启 worker 触发后台循环继续执行。
sleep 8
docker compose exec redis redis-cli ZSCORE learnq:tasks:ready "$task_id"
curl -fsS http://127.0.0.1:8080/metrics | grep learnq_reconciler_queued_enqueued_total
```

预期：Redis ready ZSet 重新出现该 task，score 是未来时间因此不会立刻被 Worker claim；指标 `learnq_reconciler_queued_enqueued_total` 增加；业务事实仍以 MySQL 中的 `queued` 任务为准。

### 旧 Worker lease 过期后提交被 fencing 拒绝

自动证据：

```bash
LEARNQ_TEST_MYSQL_DSN='learnq:learnq@tcp(127.0.0.1:3306)/learnq?parseTime=true&charset=utf8mb4&multiStatements=true' \
  GOWORK=off go test -tags=integration ./internal/store -run TestExpiredLeaseCanOnlyBeFailedByReaperPath
```

预期：过期 Worker 的常规 `Fail`/`Complete` 路径返回 `task lease lost`；只有 Reaper 专用的过期路径能推进重试或终止，避免旧结果覆盖新 generation。

### shadow rebuild 失败不切 alias

```bash
# 先确认至少有 ready 文档，并让 alias 指向当前集合。
curl -fsS http://127.0.0.1:6333/aliases | jq

# 使用不可达的 Qdrant URL 制造 shadow rebuild 失败。
QDRANT_URL=http://127.0.0.1:1 RAG_COLLECTION=learnq_live \
  GOWORK=off go run ./cmd/reindex --all --limit 1000 \
  --shadow-collection learnq_shadow_bad --alias learnq_live

curl -fsS http://127.0.0.1:6333/aliases | jq
```

预期：命令失败并输出 `alias was not changed` 或连接错误；第二次 alias 查询仍指向旧 collection。只有全部 ready 文档成功写入 shadow collection 后才会切换 alias。

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
| Qdrant 数据卷丢失或集合为空 | `/health/ready` 只代表服务可用；使用单文档重建接口或 `cmd/reindex --all` 从 MySQL 恢复向量 |
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
