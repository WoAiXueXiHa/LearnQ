# LearnQ 开发状态（2026-10-02）

> 本文持续记录开发与验证状态；§2–§4 为 10 月 1 日历史证据，10 月 2 日新增交付见 §8。目标、验收标准和阶段划分仍以 `PLAN.md` 为准；研究依据见 `STUDY.md`。

## 1. 一句话状态

**P0（检索评测）代码与无费用验证已推进，真实效果仍待验收**：现在支持冻结原文行跨度到实际索引块的映射，按完整证据点比较 dense/sparse/hybrid；新增入口默认不调用模型。核心检查、真实 MySQL/Redis 集成与隔离 Fake 端到端验证通过。**私有 Redis 原文的修订版真实检索结果尚未产生**，P0 不标记通过。P0.5 已有只读采样工具但尚未在目标服务器执行；P1–P6 产品阶段未开始。

## 2. 本轮完成的内容

| # | 内容 | 关键文件 | 验证方式 |
|---|---|---|---|
| 1 | `make eval-rag` 改为发送真实 JSONL 数据集并打印报告；支持 `EVAL_DATASET` / `BASE_URL` | `scripts/eval-rag.sh`、`Makefile` | 隔离 Fake 栈实跑，见 §3 |
| 2 | 修掉 4 条歧义标注（`rag-002/006/013/018`）：原 `citation_text` 落在 120 rune 重叠窗口，会同时命中相邻两块，服务端直接 422 拒绝整份数据集 | `data/eval/rag.jsonl` | 新增离线契约测试 + 线上 422 复现 |
| 3 | 报告升级为"汇总 + 逐例"：每题每条候选的排名、是否标注块、来源文档与行区间、块 ID、原文摘录；新增无关候选计数，头部标注模式/embedding 模型/collection/数据集哈希 | `internal/evaluation/evaluation.go` | 单测 + 实跑报告 488 行 |
| 4 | 数据集离线契约测试：用与索引器同一套切块参数复算，任何 `citation_text` 命中 0 个或多个块都让 `make test` 失败 | `internal/evaluation/dataset_test.go` | `go test ./internal/evaluation` |
| 5 | 修 API 缺陷：`POST /evaluations/rag` 用 map 插入，GORM 不回填自增主键，响应里没有 `id`，`GET …/:id/report.md` 根本无法拼接 | `internal/api/operations.go` | 实跑从 `#null` 变为 `#2` |
| 6 | 两种标注并存（`relevant_chunks` + `citation_text`）改为显式报错，不再静默忽略其中一个 | `internal/evaluation/evaluation.go` | 单测 + 线上 422 |
| 7 | 切块参数导出为 `rag.DefaultChunkSize/DefaultChunkOverlap`，索引器与离线校验共用，避免两处字面量漂移 | `internal/rag/chunk.go`、`internal/indexer/indexer.go` | `make test` |
| 8 | 本地验收命令收敛：`fmt-check/vet/test` 改按 git 索引枚举，不再被 gitignore 的 `data/reports/`、`tmp/` 历史草稿挡住 | `Makefile` | `make fmt-check && make vet && make test` 全绿 |
| 9 | `acceptance.sh` 去掉一条永远解析不到、但被静默忽略的 `citation_text`；`eval-rag.sh` 纳入 CI 的 `bash -n` | `scripts/acceptance.sh`、`.github/workflows/ci.yml` | `bash -n` |

另有两处顺带修正：`Run` 在"返回向量数不为 1"时不再把 nil 错误格式化进消息；歧义报错现在会列出命中的块 ID。

## 3. 验证证据

**静态与单测（本机）**

```
make fmt-check   # 通过
make vet         # 通过（./cmd/... ./internal/...）
make test        # 通过：agent/api/config/domain/evaluation/imagestore/indexer/model/rag/report/skill/web
GOWORK=off go test -race ./internal/evaluation ./internal/rag ./internal/indexer   # ok
node --check internal/web/static/app.js && bash -n scripts/*.sh                    # 通过
```

**端到端（Fake 模式，隔离栈：`COMPOSE_PROJECT_NAME=learnq_p0eval`、端口 18081、独立卷）**

1. 上传 `README.md` + `RUNBOOK.md` → 两篇均 `ready`；线上报告的块行区间（如 README `1-24`、`16-50`、`131-155`）与离线按同一套参数复算的结果一致，说明离线契约测试确实在复现服务端行为。
2. `BASE_URL=http://127.0.0.1:18081 make eval-rag` → 评估 `#2` 落库，报告 488 行。

| retriever | Recall@5 | NDCG | 检索覆盖率 | 无关候选 |
|---|---:|---:|---:|---:|
| dense-only | 0.6111 | 0.4016 | 0.6111 | 79 |
| sparse-only | 0.8333 | 0.5975 | 0.8333 | 75 |
| hybrid-rrf | 0.7778 | 0.6124 | 0.7778 | 76 |

> 这是 **Fake 模式（pipeline_test）** 的数字：dense 走的是假向量，只能证明链路通，**不能**当作检索质量。报告头部会显式标注模式与模型，脚本在 Fake 模式下会额外告警。

3. 失败路径（均为 422，脚本原样带回错误并退出 1）：
   - 歧义片段 → `evaluation case ambig citation_text is ambiguous: matched 2 ready chunks (<id1>, <id2>)`
   - 两种标注并存 → `evaluation case mixed sets both relevant_chunks and citation_text`

**尚未执行的验证**

- 真实 embedding（Cloudflare Qwen3 0.6B）与真实 hybrid 的对照；本轮真实模型调用 **0 次、费用 0**。
- `make acceptance` 完整复跑：本轮改动了其中的评测用例标注，但完整脚本未由我执行。撰写本文时检测到 acceptance 栈（`learnq_acceptance_115358`）已在运行，结果以其终端输出为准。
- 服务器容量（P0.5）、含图文章、五题链路，全部未开始。

## 4. 复现方式

```bash
# 离线：数据集契约（不需要数据库，不需要模型）
GOWORK=off go test ./internal/evaluation -run TestFrozenEvalDatasetResolvesUniquely -v

# 端到端：隔离 Fake 栈
export COMPOSE_PROJECT_NAME=learnq_p0eval LEARNQ_HTTP_PORT=18081 AI_MODE=fake
docker compose up --build -d
for f in README.md RUNBOOK.md; do
  curl -fsS -X POST http://127.0.0.1:18081/api/v1/documents -F "file=@$f;type=text/markdown"
done
# 等待文档 ready 后：
BASE_URL=http://127.0.0.1:18081 make eval-rag
docker compose down -v
```

## 5. P0 剩余项与下一步

1. **修订版真实 dense 逐题复测**：用私有 Redis 原文（SHA-256 `40bd20a9…77703`）重跑，确认题 1 的总结证据不再误报、题 2 找到父子进程/写时拷贝/旧快照、题 4 找到双缓冲区职责与增量追加替换。需要真实 embedding 凭据；现有入口是 `scripts/eval-redis-evidence.py`（离线 dense），原文不入户，靠 `REDIS_ARTICLE_PATH` 指向本机副本。
2. **同一 Redis 数据集的真实 Qdrant 对照**：映射工具已实现，使用 `make eval-redis-qdrant EVAL_REDIS_ARGS='--document-id <ID>'` 先进行零调用映射；授权真实预算后加 `--run` 生成三路报告。必须使用 SHA-256 匹配的 ready 原文；Fake 合成文章实跑不代替这份结果。
3. **冻结清单已更新**：现在覆盖 `rag.jsonl` 的 SHA-256，公共冻结输入有离线哈希回归。原文及旧输出仍保持私有，不提交仓库。
4. 之后按 `PLAN.md` 顺序：P0.5 服务器基础栈容量 → P1 文档版本与图片证据。

## 6. 已知问题与风险

- **Fake 指标容易被误读**：Fake 模式下 sparse > hybrid > dense 是假向量的产物，报告与脚本都已加标注，但引用数字时必须带上模式。
- **离线契约测试只覆盖冻结语料**：线上若还有别的 `ready` 文档，仍可能出现新的歧义命中；离线测试是下限不是保证。
- **MySQL `LOCATE` 大小写不敏感**：离线校验已按小写比对以贴近服务端行为，但排序规则的其他差异（重音等）仍可能有出入。
- **证据覆盖仍依赖人工标注**：新增证据点路径按完整原文跨度计数，区别于候选块行区间；它能判断已标注原文是否进入 Top K，不能证明该原文事实正确。私有 Redis 标注的真实检索验证仍未执行。
- **本地草稿文件曾让验收命令假红**：`tmp/evalinspect`、`data/reports/rag-evidence-20260921` 里各有一个不能编译的历史 Go 文件；Makefile 已改为只检查入库目录，但这些文件仍在本地，`go build ./...` 之类的裸命令依旧会失败。

## 7. 本轮产物

| 文件 | 状态 |
|---|---|
| `scripts/eval-rag.sh` | 新增（可执行，已进 CI 语法检查） |
| `internal/evaluation/dataset_test.go` | 新增 |
| `internal/evaluation/evaluation.go` | 改写报告与逐例证据 |
| `internal/api/operations.go` | 评测行改结构体插入、报告补运行元数据 |
| `internal/rag/chunk.go`、`internal/indexer/indexer.go` | 切块参数常量化 |
| `data/eval/rag.jsonl` | 4 条标注修订，SHA-256 `bf4929e6076e3881c1f1b93ada1ff972ed46ad5540600968a7a13de5a1a3b490` |
| `Makefile`、`.github/workflows/ci.yml`、`scripts/acceptance.sh`、`RUNBOOK.md` | 配套更新 |

工作区仍有上一轮未提交的 `.env.example`、`compose.yaml`、`RUNBOOK.md` 模型配置改动（目标配置改指 DeepSeek + Cloudflare，属 P6 准备），与本轮评测改动混在同一批未提交变更里。

## 8. 2026-10-02 三角色协作交付

开发 agent 实现，测试 agent 独立构造测试并运行隔离服务，review agent 复核源码、失败路径与最终报告。原有未提交改动保留，未提交运行数据或密钥；未运行生产数据清空命令。

### 已实现

- `internal/evaluation/evidence.go`：`document_id + article_sha256 + evidence_points.lines` 解析到 ready 文档实际块；核对完整文本和精确 UTF-8 字节位置。任一可接受跨度完整覆盖即可；跨块按实际召回区间并集判断，不因另一种完整组合误报。只重叠行号或召回半行不算覆盖。
- `POST /api/v1/evaluations/rag?resolve_only=true`：仅映射原文，不调用 embedding；错误版本、越界跨度、混合标注拒绝。报告保留证据点覆盖 X/Y、缺失项、原文摘录、文档与文章哈希、来源行区间和明确切块规则。
- 原始 JSONL 输入哈希与包含实际索引 ID 的解析后哈希分开；修复重复用例 ID、relevance=0 被计入必需证据、零候选吞掉缺失要点。
- `scripts/eval-redis-qdrant.py` / `make eval-redis-qdrant`：默认零调用映射，`--run` 才对每题调用一次查询 embedding。每次运行独立目录、原子保存 `run-status.json`；失败复跑不混入旧成功报告，fixture 仅读取一次。
- 冻结清单补入公共回归集哈希，`manifest_test.go` 检查公共冻结输入；`make test-tools` 接入 CI。Python 缓存不入库。
- `scripts/sample-capacity.py`：显式 Compose 项目的只读资源采样（内存、CPU、IO、OOM/重启、host swap/disk），不启动、停止或删除服务；只属于 P0.5 准备。

### 本轮验证

| 验证 | 实际结果与边界 |
|---|---|
| `make fmt-check && make vet && make test` | 通过；受限缓存时设置 `GOCACHE=/tmp/...` |
| `make race` | root 在最终 Go 修改后全套通过（`./cmd/... ./internal/...`） |
| `make test-tools` | 6 项 PASS；旧私有 Redis 原文测试 class 1 Skip，未计作原文通过 |
| JS/shell syntax、`git diff --check` | 通过 |
| 完整 Fake `make acceptance` | 隔离 `learnq_delivery_test_20261002:18102` 实跑通过，自动清理自己的卷；发生在本轮新证据功能成形前，因此另验新 API |
| tagged MySQL/Redis integration | 隔离数据库与 Redis 真实执行，133 个 PASS 行、0 Skip；清理仅限自建测试卷 |
| 新证据 API Fake 实跑 | 合成 Markdown 上传 ready → 实际块映射 → dense/sparse/hybrid，各 2/2；hash 错配、越界跨度、混合标注均 422 |
| 独立 review | 长行/跨块/替代组合、负样本、空候选、复跑混批问题已修；独立核对最终报告的输入/解析/原文哈希与摘录一致 |
| 容量工具本机 smoke | 在自建 Fake 项目实际采样 1 次成功；本机约 7.6 GiB，非目标 2 GB 服务器，不能作为容量闸门通过依据 |
| 真实模型/目标服务器/用户验收 | 未执行；付费模型调用 0 次、费用 0 |

最终 API 在最后 Go 修改后重新构建并复跑，不以旧镜像代替最终验证。隔离 Fake 入口为 `http://127.0.0.1:18103`（项目 `learnq_evidence_delivery_20261002`），保留给检查；它是现有产品与新评测 API，并没有五题练习页面。

最终合成样例报告位于 `/tmp/learnq-evidence-report/20261002T055040Z-01579cc4/report.md`，运行状态 `succeeded`，评估 ID 2；API 镜像 manifest 为 `bba943c841ffc01f4784300fa084f25a46792b61d8c0ffa326907fad2f36add7`。报告原始输入 SHA-256 为 `7d7cb43dd00d8699b3d72fc5c6cc321d651ac5140ca9d3aa989e5d72cf9b2569`，解析后 SHA-256 为 `a23807e56303fce068847f22d613700c512d3f4e2f075de93cf71cb0207c8a8c`；三路 2/2 是合成 Fake 链路验证，不是私有 Redis 或真实质量指标。

### 剩余交付条件

P0 尚需私有 Redis 原文路径、真实 embedding 凭据与明确费用上限，按冻结标注复测题 1/2/4并人工确认。P0.5 尚需目标服务器 SSH 入口和隔离目录；本轮只读检查未找到可用目标别名。按计划先完成这些闸门，再扩展 P1–P6。**五题生成、全题提交后反馈、权威来源、纠错复习和服务器产品交付均未完成，不因本轮测试通过而宣称完成。**

额外已知风险：Fake worker 的 `index_version` 仍使用配置的 embedding 模型名，可能与实际 Fake provider 不一致；本轮报告使用实际 `learnq-fake-embedding-v1` 并明确标注 `pipeline_test`。P1 索引版本契约需统一运行模型元数据。当前跨度 loader 只接受与现有 800/120 切块器一致的持久化块；未来迁移论点切块须一并升级版本/偏移契约，不能静默重建覆盖历史引用。
