#!/usr/bin/env bash
set -euo pipefail
command -v docker >/dev/null
command -v python3 >/dev/null
PRACTICE_BROWSER="${PRACTICE_BROWSER:-1}"
if [ "$PRACTICE_BROWSER" = 1 ]; then
  python3 -c 'from playwright.sync_api import sync_playwright' || {
    echo 'Playwright missing: install it in the active test environment before full browser acceptance.' >&2
    exit 1
  }
fi
# Never inherit an existing Compose project, even from the caller's environment.
export COMPOSE_PROJECT_NAME="learnq_practice_$(date +%s)_$$_${RANDOM}"
export AI_MODE=fake AI_DAILY_BUDGET_MICROCNY=0 AI_CALL_RESERVE_MICROCNY=0
export LEARNQ_HTTP_PORT="${PRACTICE_TEST_PORT:-18081}"
evidence_dir="${PRACTICE_EVIDENCE_DIR:-data/reports/practice-acceptance/${COMPOSE_PROJECT_NAME}}"
mkdir -p "$evidence_dir"
chmod 700 "$evidence_dir"
cleanup() {
  result=$?
  trap - EXIT
  docker compose logs --no-color api worker migration > "$evidence_dir/services.log" 2>&1 || true
  # These volumes belong exclusively to the random project created above.
  docker compose down -v > "$evidence_dir/cleanup.log" 2>&1 || true
  exit "$result"
}
trap cleanup EXIT
docker compose up --build -d
recovery_args=()
if [ "${PRACTICE_RECOVERY:-1}" = 1 ]; then
  recovery_args+=(--backup-restore)
fi
python3 scripts/acceptance-practice.py --base-url "http://127.0.0.1:${LEARNQ_HTTP_PORT}" --evidence-dir "$evidence_dir" --project "$COMPOSE_PROJECT_NAME" "${recovery_args[@]}"
if [ "$PRACTICE_BROWSER" = 1 ]; then
  python3 scripts/acceptance-practice-browser.py --base-url "http://127.0.0.1:${LEARNQ_HTTP_PORT}" --evidence-dir "$evidence_dir/browser"
else
  echo 'Browser explicitly disabled: this run is API-only and does not count as browser acceptance.'
fi
python3 - "$evidence_dir" "$PRACTICE_BROWSER" "${PRACTICE_RECOVERY:-1}" <<'PY'
import json
from pathlib import Path
import sys
root = Path(sys.argv[1])
stages = {"api": json.loads((root / "result.json").read_text())}
if sys.argv[2] == "1":
    stages["browser"] = json.loads((root / "browser/result.json").read_text())
if sys.argv[3] == "1":
    stages["recovery"] = json.loads((root / "recovery/result.json").read_text())
assert all(stage["status"] == "passed" for stage in stages.values()), stages
summary = {"status": "passed" if len(stages) == 3 else "passed_partial",
           "mode": "fake", "external_model_calls": 0, "quality_validated": False,
           "browser_validated": "browser" in stages, "recovery_validated": "recovery" in stages,
           "stages": {name: {"status": stage["status"], "checks": len(stage["checks"])} for name, stage in stages.items()}}
(root / "acceptance-result.json").write_text(json.dumps(summary, ensure_ascii=False, indent=2))
print("Fake acceptance:", summary["status"], summary["stages"])
PY
