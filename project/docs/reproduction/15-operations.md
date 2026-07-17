# 15 运行维护与排障

使用 `docker compose logs -f api worker migration` 观察 JSON/结构化日志，`docker compose ps -a` 检查依赖。migration 连接失败先确认 MySQL health 强制 TCP；Qdrant 官方镜像没有 wget/curl，Compose 使用 bash TCP 健康检查。

报告写入 volume 前先落 MySQL，再原子 rename 导出。备份以 MySQL 为核心；Redis 可由 reconciler 恢复，Qdrant 可删除后重新提交文档索引任务。停止使用 `docker compose down`，仅在确认删除本地数据时加 `-v`。
