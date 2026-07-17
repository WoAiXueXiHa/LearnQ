# 08 Multi-Agent 编排

Coordinator 只依据模块分类确定路由，不让模型自由决定控制流。Review 总会运行；algorithm 模块触发 Algorithm；Interview 与 Planner 可并行；Planner 汇总确定性输出。

工作流由 Eino Compose 构建。运行 `go test ./internal/skill` 验证路由、并行节点、收敛和 schema；`POST /api/v1/skills/multi-agent/runs` 可查看实际输出。
