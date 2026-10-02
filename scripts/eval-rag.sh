#!/usr/bin/env bash
# RAG 离线评估一键入口：把真实 JSONL 数据集发给已运行的 API，并打印汇总表与逐例证据。
# 数据集里的 citation_text 必须唯一命中当前 ready chunk，否则服务端会拒绝整次评测。
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:8080}"
DATASET="${EVAL_DATASET:-data/eval/rag.jsonl}"

if [ ! -f "$DATASET" ]; then
  echo "找不到评估数据集：$DATASET" >&2
  exit 1
fi

response_file=$(mktemp)
trap 'rm -f "$response_file"' EXIT

# 评测在 API 进程内同步执行，逐个问题调用 embedding；超时留足但不当成无限等待。
status=$(curl -sS --connect-timeout 3 --max-time 180 \
  -o "$response_file" -w '%{http_code}' \
  -X POST "$BASE_URL/api/v1/evaluations/rag" \
  -H 'Content-Type: application/x-ndjson' \
  --data-binary "@$DATASET")

# 422 通常是数据集本身的问题（citation_text 命中 0 个或多个 chunk），原样带回错误说明。
if [ "$status" != "202" ]; then
  echo "评估失败（HTTP $status），数据集：$DATASET" >&2
  cat "$response_file" >&2
  echo >&2
  exit 1
fi

evaluation_id=$(jq -r '.data.id' "$response_file")
mode=$(jq -r '.data.mode' "$response_file")
echo "评估 #$evaluation_id 已保存（数据集：$DATASET）"
if [ "$mode" = "pipeline_test" ]; then
  echo "注意：当前是 Fake 模式（pipeline_test），指标只证明管线连通，不代表真实检索质量。" >&2
fi

# metrics_json 是落库时序列化过的字符串，这里再解一层方便阅读。
jq -r '.data.metrics_json' "$response_file" | jq .
curl -fsS --connect-timeout 3 --max-time 30 "$BASE_URL/api/v1/evaluations/rag/$evaluation_id/report.md"
