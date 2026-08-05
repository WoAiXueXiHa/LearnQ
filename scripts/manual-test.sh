#!/bin/bash
# LearnQ 手动验收测试脚本
# 用法: bash scripts/manual-test.sh
# 前提: docker compose up -d 已启动，所有服务就绪

set -euo pipefail
command -v curl >/dev/null
command -v jq >/dev/null

# Keep a broken local connection from blocking a learning session forever.
curl() {
  command curl --connect-timeout "${CURL_CONNECT_TIMEOUT:-3}" \
    --max-time "${CURL_MAX_TIME:-120}" "$@"
}

BASE_URL="${BASE_URL:-http://127.0.0.1:8080}"
BASE="${BASE_URL%/}/api/v1"
RUN_ID="${RUN_ID:-$(date +%s)}"
IDEMPOTENCY_KEY="manual-test-${RUN_ID}"
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

PASS=0
FAIL=0

green() { echo -e "\033[32m✓ $*\033[0m"; }
red()   { echo -e "\033[31m✗ $*\033[0m"; }
info()  { echo -e "\033[36m► $*\033[0m"; }
line()  { echo "────────────────────────────────"; }

assert_status() {
  local got="$1" want="$2" label="$3"
  if [[ "$got" == "$want" ]]; then
    green "$label (status=$got)"; PASS=$((PASS+1))
  else
    red "$label — 预期 $want，实际 $got"; FAIL=$((FAIL+1))
  fi
}

assert_contains() {
  local body="$1" key="$2" label="$3"
  if [[ "$body" == *"$key"* ]]; then
    green "$label"; PASS=$((PASS+1))
  else
    red "$label — 响应中未找到 $key"; FAIL=$((FAIL+1))
  fi
}

# ── 1. 健康检查 ──
line; info "1. 健康检查"
READY=$(curl -sf "${BASE_URL%/}/health/ready")
assert_status "$(echo "$READY" | jq -r '.data.status')" "ready" "健康检查"

# ── 2. 创建学习记录 ──
line; info "2. 创建学习记录"
RECORD=$(curl -sf -X POST "$BASE/study-records" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $IDEMPOTENCY_KEY" \
  -d '{
    "title":"Go 并发与 MySQL 索引学习",
    "summary":"深入学习了 GMP 调度模型、channel 通信语义、InnoDB B+树索引结构与优化策略",
    "duration_minutes":120,
    "modules":[
      {"category":"Go","content":"GMP 调度模型：G 代表 goroutine，M 代表系统线程，P 代表处理器（逻辑 CPU）。P 的数量决定并发度。当 G 阻塞时，M 会与 P 分离，P 寻找或创建新的 M 来执行其他 G"},
      {"category":"MySQL","content":"InnoDB B+树索引：聚簇索引叶子节点存储完整行数据，二级索引叶子节点存储主键值。覆盖索引可避免回表查询。联合索引遵循最左前缀原则"},
      {"category":"algorithm","content":"接雨水问题：使用单调栈，时间复杂度 O(n)，空间复杂度 O(n)。核心思路是维护递减栈，遇到更高的柱子时弹出计算积水"}
    ]
  }')
STUDY_ID=$(echo "$RECORD" | jq -r '.data.study_record.id')
TASK_ID=$(echo "$RECORD" | jq -r '.data.task_id')
assert_contains "$RECORD" '"task_id"' "学习记录创建"
echo "  study_record_id=$STUDY_ID  task_id=$TASK_ID"

# ── 3. 等待任务完成 ──
line; info "3. 等待异步任务完成"
for i in $(seq 1 15); do
  STATUS=$(curl -sf "$BASE/tasks/$TASK_ID" | jq -r '.data.status')
  echo "  第${i}次查询: status=$STATUS"
  [[ "$STATUS" == "succeeded" ]] && break
  sleep 1
done
assert_status "$STATUS" "succeeded" "任务状态"

# ── 4. 查看任务详情 ──
line; info "4. 查看任务详情"
DETAIL=$(curl -sf "$BASE/tasks/$TASK_ID/detail")
REPORT_ID=$(echo "$DETAIL" | jq -r '.data.report.id')
REVIEW_ID=$(echo "$DETAIL" | jq -r '.data.review_task.id')
assert_contains "$DETAIL" '"report"' "任务详情含报告"
echo "  report_id=$REPORT_ID  review_task_id=$REVIEW_ID"

# ── 5. 查看 Markdown 报告 ──
line; info "5. 查看报告"
REPORT_MD=$(curl -sf "$BASE/reports/$REPORT_ID/report.md")
assert_contains "$REPORT_MD" "##" "Markdown 报告可读"

# ── 6. 查看 Trace ──
line; info "6. 查看 Trace"
TRACE=$(curl -sf "$BASE/tasks/$TASK_ID/trace")
AGENT_COUNT=$(echo "$TRACE" | jq '.data.agent_runs | length')
echo "  agent_runs: $AGENT_COUNT 条"
assert_contains "$TRACE" '"agent_runs"' "Trace 可查询"

# ── 7. 幂等性验证 ──
line; info "7. 幂等性——相同 key + 相同 body"
IDEM_OK=$(curl -sf -X POST "$BASE/study-records" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $IDEMPOTENCY_KEY" \
  -d '{
    "title":"Go 并发与 MySQL 索引学习",
    "summary":"深入学习了 GMP 调度模型、channel 通信语义、InnoDB B+树索引结构与优化策略",
    "duration_minutes":120,
    "modules":[
      {"category":"Go","content":"GMP 调度模型：G 代表 goroutine，M 代表系统线程，P 代表处理器（逻辑 CPU）。P 的数量决定并发度。当 G 阻塞时，M 会与 P 分离，P 寻找或创建新的 M 来执行其他 G"},
      {"category":"MySQL","content":"InnoDB B+树索引：聚簇索引叶子节点存储完整行数据，二级索引叶子节点存储主键值。覆盖索引可避免回表查询。联合索引遵循最左前缀原则"},
      {"category":"algorithm","content":"接雨水问题：使用单调栈，时间复杂度 O(n)，空间复杂度 O(n)。核心思路是维护递减栈，遇到更高的柱子时弹出计算积水"}
    ]
  }')
assert_status "$(echo "$IDEM_OK" | jq -r '.data.study_record.id')" "$STUDY_ID" "幂等重放保持 study_record"
assert_status "$(echo "$IDEM_OK" | jq -r '.data.task_id')" "$TASK_ID" "幂等重放保持 task"

info "  幂等性——相同 key + 不同 body → 409"
IDEM_409=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/study-records" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $IDEMPOTENCY_KEY" \
  -d '{"title":"不同的内容","summary":"不一样","duration_minutes":30,"modules":[{"category":"Go","content":"x"}]}')
assert_status "$IDEM_409" "409" "幂等冲突"

# ── 8. 技能列表 ──
line; info "8. 技能列表"
SKILLS=$(curl -sf "$BASE/skills")
SKILL_COUNT=$(echo "$SKILLS" | jq '.data | length')
echo "  技能数量: $SKILL_COUNT"
assert_contains "$SKILLS" "daily-review" "技能列表包含 daily-review"

# ── 9. Multi-Agent 工作流 ──
line; info "9. Multi-Agent 工作流（含 algorithm 模块→触发 algorithm-diagnosis）"
MULTI=$(curl -sf -X POST "$BASE/skills/multi-agent/runs" \
  -H 'Content-Type: application/json' \
  -d '{
    "title":"Go 并发与算法",
    "summary":"学习 GMP 和动态规划",
    "duration_minutes":90,
    "modules":["Go","algorithm"]
  }')
ROUTE_COUNT=$(echo "$MULTI" | jq '.data.workflow.routes | length')
assert_contains "$MULTI" '"algorithm-diagnosis"' "Multi-Agent 条件路由包含 algorithm-diagnosis"
assert_contains "$MULTI" '"weekly-plan"' "Multi-Agent 包含 weekly-plan"
echo "  执行路由数: $ROUTE_COUNT"

# ── 10. 复习任务 ──
line; info "10. 复习任务"
DUE=$(curl -sf "$BASE/review-tasks/due")
assert_contains "$DUE" '"data"' "到期复习可查询"
echo "  到期复习任务数: $(echo "$DUE" | jq '.data | length')"

# ── 11. 未到期复习保护 ──
if [[ -n "$REVIEW_ID" && "$REVIEW_ID" != "null" ]]; then
  info "  新报告一天后到期，立即完成应返回 REVIEW_NOT_DUE"
  REVIEW_BODY="$TMP_DIR/review-not-due.json"
  REVIEW_STATUS=$(curl -sS -o "$REVIEW_BODY" -w '%{http_code}' \
    -X POST "$BASE/review-tasks/$REVIEW_ID/complete" \
    -H 'Content-Type: application/json' \
    -d '{"mastery": 1}')
  assert_status "$REVIEW_STATUS" "409" "未到期复习保护"
  assert_contains "$(cat "$REVIEW_BODY")" '"code":"REVIEW_NOT_DUE"' "错误语义明确"
fi

# ── 12. 周统计 ──
line; info "12. 周统计"
WEEKLY=$(curl -sf "$BASE/analytics/weekly")
assert_contains "$WEEKLY" '"summary"' "周统计可查询"
echo "  本周学习: $(echo "$WEEKLY" | jq '.data.summary')"

# ── 13. RAG 文档上传 ──
line; info "13. RAG 文档上传"
RAG_DOC="$TMP_DIR/rag-test-doc.md"
cat > "$RAG_DOC" << 'DOCEOF'
# LearnQ 异步任务管线

## 防止旧 Worker 覆盖新结果

LearnQ 通过 lease fencing 机制防止旧 Worker 结果覆盖新结果：
- execution_generation 每次重试递增
- lease_token 随机生成，Reaper 可清空
- lease_until 租约截止时间，Complete 时检查是否过期
- Complete 的 UPDATE 包含 WHERE lease_token=? AND lease_until>=? AND execution_generation=?

## 检索方案

Qdrant 使用 dense + sparse 双路预取，通过 RRF 融合排序。
dense: 语义向量 top-20；sparse: FNV-1a + log-TF 词法检索 top-20。
DOCEOF

DOC_UPLOAD=$(curl -sf -X POST "$BASE/documents" -F "file=@$RAG_DOC")
DOC_ID=$(echo "$DOC_UPLOAD" | jq -r '.data.document_id')
IDX_TASK=$(echo "$DOC_UPLOAD" | jq -r '.data.indexing_task_id')
assert_contains "$DOC_UPLOAD" '"status":"uploaded"' "文档上传"
echo "  document_id=$DOC_ID  indexing_task_id=$IDX_TASK"

# 等待索引完成
info "  等待文档索引..."
for i in $(seq 1 15); do
  DOC_STATUS=$(curl -sf "$BASE/documents/$DOC_ID/status" | jq -r '.data.status')
  echo "  第${i}次查询: status=$DOC_STATUS"
  [[ "$DOC_STATUS" == "ready" ]] && break
  sleep 1
done
assert_status "$DOC_STATUS" "ready" "文档索引完成"

# ── 14. RAG 查询 ──
line; info "14. RAG 查询"
RAG=$(curl -sf -X POST "$BASE/rag/query" \
  -H 'Content-Type: application/json' \
  -d '{"question":"LearnQ 如何防止旧 Worker 覆盖新结果？","top_k":5}')
CITE_COUNT=$(echo "$RAG" | jq '.data.citations | length')
CHUNK_ID=$(echo "$RAG" | jq -r '.data.citations[0].chunk_id')
assert_contains "$RAG" '"citations"' "RAG 检索"
echo "  引用数量: $CITE_COUNT"

# ── 15. RAG 评估 ──
line; info "15. RAG 评估"
EVAL=$(printf '{"id":"manual-rag","question":"LearnQ 如何防止旧 Worker 覆盖新结果？","relevant_chunks":[{"chunk_id":"%s","relevance":3}],"citation_text":"lease fencing","correct_answer":"使用 generation、lease token 和 lease_until 条件更新","tags":["worker"],"difficulty":"easy"}\n' "$CHUNK_ID" |
  curl -sf -X POST "$BASE/evaluations/rag" \
    -H 'Content-Type: application/jsonl' --data-binary @-)
RETRIEVERS=$(echo "$EVAL" | jq -r '.data.metrics_json | fromjson | keys')
assert_contains "$RETRIEVERS" "hybrid-rrf" "评估含 hybrid-rrf"
echo "  评估模式: $(echo "$EVAL" | jq -r '.data.mode')"
echo "  检索器: $RETRIEVERS"

# ── 汇总 ──
line
echo "通过: $PASS  失败: $FAIL"
if [[ $FAIL -eq 0 ]]; then
  green "全部测试通过！"
else
  red "存在 $FAIL 项失败，请检查日志"
  exit 1
fi
