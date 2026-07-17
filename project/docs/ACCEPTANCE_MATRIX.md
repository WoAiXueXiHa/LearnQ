# LearnQ 验收矩阵

状态：⬜ 未实现；🟨 已实现待完整环境验证；✅ 已验证。

| 编号 | 能力 | 验收证据 | 状态 |
|---|---|---|---|
| AQ-001 | 学习记录、模块、Task、Outbox 同事务 | store rollback/commit 集成测试 | ✅ |
| AQ-002 | Outbox SKIP LOCKED 与崩溃恢复 | replay/concurrent dispatcher 集成测试 | ✅ |
| AQ-003 | Redis TIME + Lua 原子领取 | 20 并发领取集成测试 | ✅ |
| AQ-004 | lease fencing 与并发执行权 | stale token/并发 acquire 集成测试 | ✅ |
| AQ-005 | 45s 超时、2s/4s 退避、dead | 状态机与 worker 集成测试 | ✅ |
| AQ-006 | Reaper、Reconciler、优雅停机 | recovery 集成测试 | ✅ |
| AQ-007 | Fake AI 与 Schema 校验/故障注入 | timeout/temporary/invalid JSON/schema 单元测试 | ✅ |
| AQ-008 | 报告事实源与原子导出 | Compose 中 export succeeded 且卷内文件可读 | ✅ |
| AQ-009 | 复习间隔、完成、跳过与事件 | domain/API 集成测试 | ✅ |
| AQ-010 | 五个版本化 Skill | registry/运行测试 | ✅ |
| AQ-011 | 确定性 Multi-Agent 条件编排 | workflow 测试 | ✅ |
| AQ-012 | Agent/Tool/检索/Token 轨迹 | weekly_stats/rag_query 真实执行与 trace 语义断言 | ✅ |
| AQ-013 | 三类文档异步索引 | upload/index/失败恢复集成测试 | ✅ |
| AQ-014 | dense+sparse+Qdrant RRF | adapter 测试 + Qdrant 1.18.2 E2E | ✅ |
| AQ-015 | 可验证引用与无证据拒答 | API 集成测试 + ready 文档引用 E2E | ✅ |
| AQ-016 | 三组离线 RAG 指标 | 三路实际 Qdrant 评估报告 | ✅ |
| AQ-017 | API envelope、错误与幂等键 | HTTP 集成测试 | ✅ |
| AQ-018 | 中文 Web 全链路 | 记录/报告/复习/Skill/上传/RAG 语义测试 | ✅ |
| AQ-019 | Compose 一键运行与健康等待 | 扩展 `scripts/acceptance.sh` 全链路通过 | ✅ |
| AQ-020 | GOWORK=off build/test | 命令输出 | ✅ |
| AQ-021 | README 与 15 章复现文档 | README + docs/reproduction/01..15 | ✅ |
