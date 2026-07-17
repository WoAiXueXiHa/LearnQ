# 05 Redis 调度队列

调度队列使用 ZSet，score 是 `available_at`。Lua 脚本内部调用 Redis `TIME`，读取到期任务、写 lease key 并移除 ZSet 成员，避免客户端时钟漂移和并发双领取。

运行 `go test -tags=integration ./internal/queue -count=1` 可观察 20 个并发领取者只有一个成功。Redis 数据丢失后 reconciler 从 MySQL 的 pending/retry_wait 任务恢复。
