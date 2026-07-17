#!/bin/sh
set -eu
command -v curl >/dev/null
command -v jq >/dev/null
docker compose up --build -d
trap 'docker compose down -v' EXIT
attempt=0
until curl -fsS http://127.0.0.1:8080/health/ready >/dev/null; do
  attempt=$((attempt+1))
  [ "$attempt" -lt 60 ] || { docker compose logs; exit 1; }
  sleep 2
done
response=$(curl -fsS -X POST http://127.0.0.1:8080/api/v1/study-records \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: acceptance-001' \
  -d '{"title":"验收记录","summary":"Fake 模式","duration_minutes":30,"modules":[{"category":"algorithm","content":"二分搜索"}]}')
echo "$response"
task_id=$(printf '%s' "$response" | jq -r '.data.task_id')
attempt=0
while :; do
  task_status=$(curl -fsS "http://127.0.0.1:8080/api/v1/tasks/$task_id" | jq -r '.data.status')
  [ "$task_status" = succeeded ] && break
  [ "$task_status" != dead ] || exit 1
  attempt=$((attempt+1))
  [ "$attempt" -lt 60 ] || exit 1
  sleep 1
done
curl -fsS "http://127.0.0.1:8080/api/v1/tasks/$task_id/trace" | jq -e '.data.agent_runs | length > 0' >/dev/null
detail=$(curl -fsS "http://127.0.0.1:8080/api/v1/tasks/$task_id/detail")
printf '%s' "$detail" | jq -e '.data.report.export_status == "succeeded" and .data.review_task.status == "scheduled"' >/dev/null
report_id=$(printf '%s' "$detail" | jq -r '.data.report.id')
docker compose exec -T worker test -r "/app/data/reports/$report_id.md"
curl -fsS 'http://127.0.0.1:8080/api/v1/review-tasks?scope=upcoming' | jq -e '.data | length > 0' >/dev/null

duplicate=$(curl -fsS -X POST http://127.0.0.1:8080/api/v1/study-records \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: acceptance-duplicate' \
  -d '{"title":"验收记录","summary":"Fake 模式","duration_minutes":30,"modules":[{"category":"algorithm","content":"二分搜索"}]}')
printf '%s' "$duplicate" | jq -e --argjson task "$task_id" '.data.deduplicated == true and .data.task_id == $task' >/dev/null

curl -fsS http://127.0.0.1:8080/api/v1/skills
daily=$(curl -fsS -X POST http://127.0.0.1:8080/api/v1/skills/daily-review/runs -H 'Content-Type: application/json' -d '{"title":"验收记录","summary":"二分搜索"}')
weekly=$(curl -fsS -X POST http://127.0.0.1:8080/api/v1/skills/weekly-plan/runs -H 'Content-Type: application/json' -d '{"title":"本周计划"}')
[ "$(printf '%s' "$daily" | jq -r '.data.output.title')" != "$(printf '%s' "$weekly" | jq -r '.data.output.title')" ]
weekly_run=$(printf '%s' "$weekly" | jq -r '.data.agent_run_id')
curl -fsS "http://127.0.0.1:8080/api/v1/agent-runs/$weekly_run" |
  jq -e '.data.tool_calls[0].response_json | contains("\"fact_source\":\"mysql\"")' >/dev/null

upload=$(curl -fsS -X POST http://127.0.0.1:8080/api/v1/documents -F 'file=@README.md;type=text/markdown')
document_id=$(printf '%s' "$upload" | jq -r '.data.document_id')
attempt=0
while :; do
  document_status=$(curl -fsS "http://127.0.0.1:8080/api/v1/documents/$document_id/status" | jq -r '.data.status')
  [ "$document_status" = ready ] && break
  [ "$document_status" != failed ] || exit 1
  attempt=$((attempt+1))
  [ "$attempt" -lt 60 ] || exit 1
  sleep 1
done
curl -fsS http://127.0.0.1:8080/api/v1/documents | jq -e '.data[0].status == "ready"' >/dev/null

rag=$(curl -fsS -X POST http://127.0.0.1:8080/api/v1/rag/query \
  -H 'Content-Type: application/json' -d '{"question":"LearnQ 如何启动 Compose？","top_k":5}')
printf '%s' "$rag" | jq -e '.data.answer | contains("[S1]")' >/dev/null
chunk_id=$(printf '%s' "$rag" | jq -r '.data.citations[0].chunk_id')
printf '{"id":"acceptance-rag","question":"LearnQ 如何启动 Compose？","relevant_chunks":[{"chunk_id":"%s","relevance":3}],"citation_text":"Compose 启动说明","correct_answer":"使用 docker compose 启动","tags":["compose"],"difficulty":"easy"}\n' "$chunk_id" |
  curl -fsS -X POST http://127.0.0.1:8080/api/v1/evaluations/rag \
    -H 'Content-Type: application/jsonl' --data-binary @- |
  jq -e '.data.status == "succeeded"' >/dev/null

curl -fsS http://127.0.0.1:8080/api/v1/analytics/weekly | jq -e '.data.fact_source == "mysql"' >/dev/null
echo "LearnQ acceptance passed"
