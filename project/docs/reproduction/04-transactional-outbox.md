# 04 Transactional Outbox

创建学习记录时，`study_records`、`study_modules`、`ai_tasks` 与 `outbox_events` 在同一个 MySQL 事务提交。任一写入失败会整体回滚。

dispatcher 使用 `FOR UPDATE SKIP LOCKED` 并发扫描未发布事件，通过幂等 ZADD 写 Redis，最后在同一数据库事务标记 task queued 与 outbox published。进程在两者之间崩溃时，重放仍收敛。
