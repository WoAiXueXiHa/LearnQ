# 14 测试与故障验证

依次运行 `make fmt-check`、`make vet`、`make test`。准备 MySQL/Redis 后运行 `make test-integration`；测试覆盖事务回滚、outbox 重放、并发领取、stale fencing、retry/dead、reaper/reconciler 和异步索引恢复。

运行 `make acceptance` 会构建 Compose、等待健康、创建任务并等待报告、检查 trace、上传并索引文档、执行真实 Qdrant RAG、运行三路评估和 SQL 周统计，最后清理验收卷。
