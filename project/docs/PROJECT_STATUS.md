# 项目状态

更新时间：2026-07-17

| 阶段 | 状态 | 当前结果 |
|---|---|---|
| 仓库勘察 | 完成 | demo 保持独立；LearnQ 全部实现位于 project；Go 与 Docker/Compose 可用 |
| P0 | 完成 | 事务 Outbox、Redis TIME/Lua、Worker、fencing、retry/dead、recovery、报告与复习均通过依赖集成测试 |
| P1 | 完成 | 五 Skill、Eino 确定性条件工作流、Fake/Real 共用链路和完整 trace 通过单元/集成及 Compose 验收 |
| P2 | 完成 | 三类文档异步索引、失败恢复、Qdrant dense+sparse RRF、引用过滤及三路离线评估通过真实容器验收 |
| P3 | 完成 | 中文用户闭环已重建：记录防重、Task 自动轮询、报告/trace、复习计划、差异化 Skill、文档/RAG；Compose 语义验收通过 |

## 环境事实

- 所有原始文件在开始时均为未跟踪文件；实现不覆盖它们。
- `demo/` 不引用 `project/`，`project/` 不引用 `demo/`。
- Docker 29.1.3、Compose v5.1.4 已验证；Docker Hub 直连超时，首次镜像通过代理拉取并标记，本地 Compose 使用官方镜像名。
- Compose 健康检查已修复：MySQL 强制 TCP，Qdrant 使用镜像内置 bash 的 TCP 检查。

## 已执行门禁

- `GOWORK=off go test ./...`：通过。
- `GOWORK=off go vet ./...`：通过。
- `GOWORK=off go build ./...`：通过。
- `go test -p 1 -tags=integration ./... -count=1`：通过（MySQL 8.4、Redis）。
- `bash scripts/acceptance.sh`：通过；覆盖 Compose 构建/健康等待、异步任务、trace、文档索引、真实 Qdrant RAG、三路评估与 SQL 周统计。
- 用户闭环复验：10 分钟相同内容复用、报告卷文件可读、upcoming 复习可见、Skill 产物差异化、真实工具响应进入 trace。
- 实际评估样例：dense/sparse Recall@5=1、NDCG=0.6309；hybrid RRF Recall@5=1、NDCG=1；三路引用覆盖率均为 1。
