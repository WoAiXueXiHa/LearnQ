#!/usr/bin/env python3
"""Build frozen Redis span annotations and evaluate the live Qdrant paths.

Default resolves persisted chunks only (zero embedding calls). --run invokes
one query embedding per question using the API's configured provider; never uploads articles.
"""
import argparse
import hashlib
import json
import os
import uuid
from datetime import datetime, timezone
from pathlib import Path
import urllib.error
import urllib.request


def build_cases(fixture, document_id):
    if document_id <= 0:
        raise ValueError("document-id must be positive")
    return [dict(id=q["id"], question=q["question"], document_id=document_id,
                 article_sha256=fixture["article_sha256"],
                 evidence_points=q["evidence"])
            for q in fixture["questions"]]


def request(url, body=None):
    headers = {"Content-Type": "application/x-ndjson"}
    req = urllib.request.Request(url, data=body, headers=headers)
    with urllib.request.urlopen(req, timeout=180) as response:
        return response.read()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--document-id", type=int, required=True,
                        help="existing ready document uploaded through the normal API")
    parser.add_argument("--fixture", default="data/eval/redis_persistence_evidence.json")
    parser.add_argument("--base-url", default=os.environ.get("BASE_URL", "http://127.0.0.1:8080"))
    parser.add_argument("--output-dir", default="data/reports/redis_persistence/qdrant")
    parser.add_argument("--run", action="store_true", help="explicitly run one query embedding per question")
    args = parser.parse_args()
    fixture_bytes = Path(args.fixture).read_bytes()
    fixture = json.loads(fixture_bytes.decode("utf-8"))
    cases = build_cases(fixture, args.document_id)
    body = "".join(json.dumps(c, ensure_ascii=False) + "\n" for c in cases).encode()
    base = args.base_url.rstrip("/")
    output = Path(args.output_dir) / (datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8])
    output.mkdir(parents=True, exist_ok=False)
    state = dict(status="started", dataset_sha256=hashlib.sha256(body).hexdigest(),
                 fixture_sha256=hashlib.sha256(fixture_bytes).hexdigest(),
                 document_id=args.document_id, question_count=len(cases), run_requested=args.run)
    def save_state():
        status_tmp = output / "run-status.json.tmp"
        status_tmp.write_text(json.dumps(state, indent=2), encoding="utf-8")
        status_tmp.replace(output / "run-status.json")
    (output / "dataset.jsonl").write_bytes(body)
    save_state()
    print(f"Run artifacts: {output}")
    try:
        # Resolve before retrieval so mismatched hashes and unsupported spans fail free.
        resolved = json.loads(request(base + "/api/v1/evaluations/rag?resolve_only=true", body))
        data = resolved.get("data")
        if not isinstance(data, list) or [c.get("id") for c in data] != [c["id"] for c in cases]:
            raise ValueError("resolution response does not match all input cases")
        if any(not c.get("relevant_chunks") or not c.get("evidence_points") for c in data):
            raise ValueError("resolution response misses mapped evidence")
        (output / "resolved.json").write_text(json.dumps(resolved, ensure_ascii=False, indent=2), encoding="utf-8")
        state["status"] = "resolved"
        save_state()
        print(f"Mapped {len(cases)} questions against actual document_chunks; no embedding calls.")
        print(f'Dataset SHA-256: {state["dataset_sha256"]}')
        if args.run:
            result = json.loads(request(base + "/api/v1/evaluations/rag", body))["data"]
            report = request(base + f'/api/v1/evaluations/rag/{result["id"]}/report.md')
            (output / "report.md").write_bytes(report)
            state.update(status="succeeded", evaluation_id=result["id"], mode=result["mode"])
            save_state()
            print(report.decode())
        else:
            print(f"Re-run with --run to execute dense/sparse/hybrid ({len(cases)} query embeddings).")
    except Exception as error:
        state.update(status="failed", error_type=type(error).__name__)
        # Omit arbitrary remote exception text from artifacts to avoid persisting credentials.
        save_state()
        raise



if __name__ == "__main__":
    try:
        main()
    except urllib.error.HTTPError as error:
        print(f"HTTP {error.code}: {error.read().decode()}", file=__import__("sys").stderr)
        raise SystemExit(1)
    except (ValueError, KeyError, OSError, urllib.error.URLError) as error:
        print(f"Evaluation failed: {error}", file=__import__("sys").stderr)
        raise SystemExit(1)
