#!/bin/sh
set -eu
command -v curl >/dev/null
command -v jq >/dev/null

# A stalled local socket must fail the assertion instead of hanging CI forever.
curl() {
  command curl --connect-timeout "${CURL_CONNECT_TIMEOUT:-3}" \
    --max-time "${CURL_MAX_TIME:-20}" "$@"
}

COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-learnq_acceptance_$$}"
LEARNQ_HTTP_PORT="${LEARNQ_HTTP_PORT:-18080}"
AI_MODE=fake
BASE_URL="http://127.0.0.1:${LEARNQ_HTTP_PORT}"
export COMPOSE_PROJECT_NAME LEARNQ_HTTP_PORT AI_MODE
docker compose up --build -d
image_file=$(mktemp)
cleanup() {
  status=$?
  if [ "$status" -ne 0 ]; then
    docker compose logs api worker || true
  fi
  rm -f "$image_file"
  docker compose down -v
  exit "$status"
}
trap cleanup EXIT
attempt=0
while :; do
  if curl -fsS "$BASE_URL/health/ready" >/dev/null; then
    break
  fi
  attempt=$((attempt+1))
  [ "$attempt" -lt 60 ] || { docker compose logs; exit 1; }
  sleep 2
done
curl -fsS "$BASE_URL/health/ready" | jq -e '.data.dependencies.worker == "ok"' >/dev/null
home=$(curl -fsS "$BASE_URL/")
case "$home" in *'id="recordForm"'*) ;; *) exit 1 ;; esac
app_js=$(curl -fsS "$BASE_URL/static/app.js")
case "$app_js" in *taskPollFailures*) ;; *) exit 1 ;; esac
for scope in due upcoming all; do
  curl -fsS "$BASE_URL/api/v1/review-tasks?scope=$scope" |
    jq -e '.data | type == "array" and length == 0' >/dev/null
done
curl -fsS "$BASE_URL/api/v1/documents" | jq -e '.data | type == "array" and length == 0' >/dev/null
response=$(curl -fsS -X POST "$BASE_URL/api/v1/study-records" \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: acceptance-001' \
  -d '{"title":"验收记录","summary":"Fake 模式","duration_minutes":30,"modules":[{"category":"algorithm","content":"二分搜索"}]}')
echo "$response"
task_id=$(printf '%s' "$response" | jq -r '.data.task_id')
attempt=0
while :; do
  task_status=$(curl -fsS "$BASE_URL/api/v1/tasks/$task_id" | jq -r '.data.status')
  [ "$task_status" = succeeded ] && break
  [ "$task_status" != dead ] || exit 1
  attempt=$((attempt+1))
  [ "$attempt" -lt 60 ] || exit 1
  sleep 1
done
curl -fsS "$BASE_URL/api/v1/tasks/$task_id/trace" | jq -e '.data | (.agent_runs | length == 1) and .agent_runs[0].skill_name == "daily-review"' >/dev/null
detail=$(curl -fsS "$BASE_URL/api/v1/tasks/$task_id/detail")
printf '%s' "$detail" | jq -e '.data.report.export_status == "succeeded" and .data.review_task.status == "scheduled"' >/dev/null
printf '%s' "$detail" | jq -e '.data.report.markdown_content | contains("面试追问") and (contains("_tool_results") | not)' >/dev/null
report_id=$(printf '%s' "$detail" | jq -r '.data.report.id')
docker compose exec -T worker test -r "/app/data/reports/$report_id.md"
curl -fsS "$BASE_URL/api/v1/review-tasks?scope=upcoming" | jq -e '.data | length > 0' >/dev/null

duplicate=$(curl -fsS -X POST "$BASE_URL/api/v1/study-records" \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: acceptance-duplicate' \
  -d '{"title":"验收记录","summary":"Fake 模式","duration_minutes":30,"modules":[{"category":"algorithm","content":"二分搜索"}]}')
printf '%s' "$duplicate" | jq -e --argjson task "$task_id" '.data.deduplicated == true and .data.task_id == $task' >/dev/null

curl -fsS "$BASE_URL/api/v1/skills"
workflow=$(curl -fsS -X POST "$BASE_URL/api/v1/skills/multi-agent/runs" -H 'Content-Type: application/json' \
  -d '{"title":"验收实验","summary":"独立 Agent 输出","modules":["algorithm","project"]}')
printf '%s' "$workflow" | jq -e '.data.workflow.outputs["algorithm-diagnosis"].Content and .data.agent_run_ids["weekly-plan"]' >/dev/null
daily=$(curl -fsS -X POST "$BASE_URL/api/v1/skills/daily-review/runs" -H 'Content-Type: application/json' -d '{"title":"验收记录","summary":"二分搜索"}')
weekly=$(curl -fsS -X POST "$BASE_URL/api/v1/skills/weekly-plan/runs" -H 'Content-Type: application/json' -d '{"title":"本周计划"}')
[ "$(printf '%s' "$daily" | jq -r '.data.output.title')" != "$(printf '%s' "$weekly" | jq -r '.data.output.title')" ]
weekly_run=$(printf '%s' "$weekly" | jq -r '.data.agent_run_id')
curl -fsS "$BASE_URL/api/v1/agent-runs/$weekly_run" |
  jq -e '.data.tool_calls[0].response_json | contains("\"fact_source\":\"mysql\"")' >/dev/null

upload=$(curl -fsS -X POST "$BASE_URL/api/v1/documents" -F 'file=@README.md;type=text/markdown')
document_id=$(printf '%s' "$upload" | jq -r '.data.document_id')
attempt=0
while :; do
  document_status=$(curl -fsS "$BASE_URL/api/v1/documents/$document_id/status" | jq -r '.data.status')
  [ "$document_status" = ready ] && break
  [ "$document_status" != failed ] || exit 1
  attempt=$((attempt+1))
  [ "$attempt" -lt 60 ] || exit 1
  sleep 1
done
curl -fsS "$BASE_URL/api/v1/documents" | jq -e '.data[0].status == "ready"' >/dev/null
indexing_task_id=$(printf '%s' "$upload" | jq -r '.data.indexing_task_id')
curl -fsS "$BASE_URL/api/v1/tasks/$indexing_task_id/detail" |
  jq -e '.data.task.kind == "document_index" and .data.document.status == "ready" and .data.report == null' >/dev/null

printf '%s' 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=' |
  base64 -d >"$image_file"
image_upload=$(curl -fsS -X POST "$BASE_URL/api/v1/images" \
  -F "file=@$image_file;filename=acceptance.png;type=image/png" \
  -F 'prompt=提取这张测试图片的学习重点')
image_id=$(printf '%s' "$image_upload" | jq -r '.data.id')
image_task_id=$(printf '%s' "$image_upload" | jq -r '.data.task_id')
attempt=0
while :; do
  image_status=$(curl -fsS "$BASE_URL/api/v1/images/$image_id" | jq -r '.data.status')
  [ "$image_status" = ready ] && break
  [ "$image_status" != failed ] || exit 1
  attempt=$((attempt+1))
  [ "$attempt" -lt 60 ] || exit 1
  sleep 1
done
curl -fsS "$BASE_URL/api/v1/tasks/$image_task_id/detail" |
  jq -e '.data.task.kind == "image_describe" and .data.image.status == "ready" and .data.image.description.summary != ""' >/dev/null
curl -fsS "$BASE_URL/api/v1/images/$image_id/content" -o /dev/null
image_index=$(curl -fsS -X POST "$BASE_URL/api/v1/images/$image_id/index")
image_document_id=$(printf '%s' "$image_index" | jq -r '.data.document_id')
test "$image_document_id" -gt 0
curl -fsS -X POST "$BASE_URL/api/v1/images/$image_id/index" |
  jq -e --argjson document "$image_document_id" '.data.document_id == $document' >/dev/null
attempt=0
while :; do
  image_document_status=$(curl -fsS "$BASE_URL/api/v1/documents/$image_document_id/status" | jq -r '.data.status')
  [ "$image_document_status" = ready ] && break
  [ "$image_document_status" != failed ] || exit 1
  attempt=$((attempt+1))
  [ "$attempt" -lt 60 ] || exit 1
  sleep 1
done

rag=$(curl -fsS -X POST "$BASE_URL/api/v1/rag/query" \
  -H 'Content-Type: application/json' -d '{"question":"LearnQ 如何启动 Compose？","top_k":5}')
printf '%s' "$rag" | jq -e '.data.answer | contains("[S1]")' >/dev/null
chunk_id=$(printf '%s' "$rag" | jq -r '.data.citations[0].chunk_id')
printf '{"id":"acceptance-rag","question":"LearnQ 如何启动 Compose？","relevant_chunks":[{"chunk_id":"%s","relevance":3}],"citation_text":"Compose 启动说明","correct_answer":"使用 docker compose 启动","tags":["compose"],"difficulty":"easy"}\n' "$chunk_id" |
  curl -fsS -X POST "$BASE_URL/api/v1/evaluations/rag" \
    -H 'Content-Type: application/jsonl' --data-binary @- |
  jq -e '.data.status == "succeeded"' >/dev/null

curl -fsS -X DELETE "$BASE_URL/api/v1/images/$image_id" |
  jq -e --argjson image "$image_id" '.data.deleted == $image' >/dev/null
[ "$(curl -sS -o /dev/null -w '%{http_code}' "$BASE_URL/api/v1/images/$image_id")" = 404 ]
curl -fsS "$BASE_URL/api/v1/documents" |
  jq -e --argjson document "$image_document_id" '[.data[].id] | index($document) == null' >/dev/null

curl -fsS "$BASE_URL/api/v1/analytics/weekly" | jq -e '.data.fact_source == "mysql"' >/dev/null

docker compose stop worker
attempt=0
while :; do
  stopped_health=$(curl -sS "$BASE_URL/health/ready")
  printf '%s' "$stopped_health" | jq -e '.error.details.dependencies.worker == "unavailable"' >/dev/null && break
  attempt=$((attempt+1))
  [ "$attempt" -lt 20 ] || { printf '%s\n' "$stopped_health"; exit 1; }
  sleep 1
done
docker compose start worker
attempt=0
while :; do
  if curl -fsS "$BASE_URL/health/ready" | jq -e '.data.dependencies.worker == "ok"' >/dev/null; then
    break
  fi
  attempt=$((attempt+1))
  [ "$attempt" -lt 20 ] || exit 1
  sleep 1
done

echo "LearnQ acceptance passed"
