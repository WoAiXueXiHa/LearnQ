# 02 架构与事实边界

MySQL 保存业务事实、任务、attempt、outbox、报告、复习、文档元数据和 trace。Redis 只保存可重建的调度索引，Qdrant 只保存可重建的向量索引。

API 在事务内写业务行、AI task 和 outbox；dispatcher 发布到 Redis；worker 领取租约并写回 MySQL。任何 Redis/Qdrant 丢失都可以从 MySQL 重放或重建。
