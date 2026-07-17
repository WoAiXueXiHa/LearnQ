# 09 Trace 与审计

每次执行记录 agent_runs、agent_steps 与 tool_calls，包括 task、skill/prompt/schema 版本、prompt hash、模型、token、耗时、输入摘要、输出 JSON 和错误原因。

写入在一个 MySQL 事务与连接内取得 insert id，避免并发关联错位。通过 `GET /api/v1/tasks/{id}/trace` 联合查看 attempt、run、step 和 tool 轨迹。
