# 06 Worker 可靠性

Worker 默认并发 4、单任务硬超时 45 秒、lease 60 秒。领取后 MySQL 条件更新把 queued 改为 processing 并记录 token；完成写入同时检查状态、generation、token 与 lease 时间。

失败最多执行三次，前两次按 2 秒、4 秒退避，第三次进入 dead。Reaper 回收过期 processing；人工 retry 增加 generation，旧 worker 因 fencing 条件无法覆盖新结果。
