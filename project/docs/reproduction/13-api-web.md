# 13 API 与 Web

API 使用 `/api/v1`，成功响应包含 data/request_id，失败响应包含 code、message、request_id 与可选 details。创建学习记录支持 Idempotency-Key；同键不同请求返回冲突。

根路径提供中文 Go Template + 原生 JS 页面。页面从记录创建开始自动轮询 Task，直接渲染报告、复习安排与 Agent/tool trace；复习按到期/即将到期分组，知识库支持上传、索引状态、删除与引用卡片，Skill 提供差异化输入和结果。

相同内容在 10 分钟内默认复用已有任务，用户仍可显式新建。`/health/live` 检查进程，`/health/ready` 检查 MySQL；聚合 Task 详情、Agent run、文档列表和复习范围查询避免前端拼接原始 JSON。
