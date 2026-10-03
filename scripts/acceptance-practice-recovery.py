#!/usr/bin/env python3
"""Cold-backup and business-restore acceptance of a synthetic Fake stack."""
import argparse
import base64
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import time
import urllib.request
from urllib.parse import urlparse


def request(base, path, body=None):
    payload = None if body is None else json.dumps(body).encode()
    headers = {} if body is None else {"Content-Type": "application/json"}
    with urllib.request.urlopen(urllib.request.Request(base + path, payload, headers), timeout=30) as response:
        return json.load(response)["data"]


def run(args):
    if not args.project.startswith("learnq_practice_"):
        raise ValueError("only a dedicated practice acceptance project may be stopped")
    if request(args.base_url, "/api/v1/model-budget")["mode"] != "fake":
        raise ValueError("Fake source required")
    args.evidence_dir.mkdir(parents=True, exist_ok=False)
    def wait(path, ready):
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            value = request(args.base_url, path)
            if ready(value):
                return value
            time.sleep(1)
        raise AssertionError("fixture task timed out: " + path)

    # Include nonempty report/image volumes in the cold-backup proof.
    learning = request(args.base_url, "/api/v1/study-records", {
        "title": "恢复演练合成记录", "summary": "Fake 全栈恢复", "duration_minutes": 1,
        "modules": [{"category": "backend", "content": "事务恢复"}]})
    task_path = f'/api/v1/tasks/{learning["task_id"]}/detail'
    learning_detail = wait(task_path, lambda v: v["task"]["status"] == "succeeded")
    image = base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
    boundary = "LearnQRecoveryImage"
    upload = (f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="recovery.png"\r\nContent-Type: image/png\r\n\r\n'.encode()
              + image + f'\r\n--{boundary}--\r\n'.encode())
    with urllib.request.urlopen(urllib.request.Request(args.base_url + "/api/v1/images", upload,
                                {"Content-Type": "multipart/form-data; boundary=" + boundary}), timeout=30) as response:
        image_id = json.load(response)["data"]["id"]
    image_path = f"/api/v1/images/{image_id}"
    wait(image_path, lambda v: v["status"] == "ready")
    attempt_path = f"/api/v1/practice-attempts/{args.attempt_id}"
    document_path = f"/api/v1/documents/{args.document_id}/status"
    before = request(args.base_url, attempt_path)
    document = request(args.base_url, document_path)
    evidence_url = f'/api/v1/documents/{args.document_id}/indexes/{before["index_id"]}/chunks/{args.chunk_id}'
    historical = request(args.base_url, evidence_url)
    result = {"status": "failed", "checks": [], "external_model_calls": 0, "quality_validated": False}
    restore_dir = args.evidence_dir / "restore"
    commands = [sys.executable, str(Path(__file__).with_name("backup-stack.py")), "--project", args.project,
                "--maintenance", "--output", str(args.evidence_dir / "backup")]
    try:
        with (args.evidence_dir / "backup.log").open("w") as log:
            subprocess.run(commands, check=True, stdout=log, stderr=log, timeout=900)
        with (args.evidence_dir / "restore.log").open("w") as log:
            subprocess.run([sys.executable, str(Path(__file__).with_name("restore-stack-isolated.py")),
                            "--backup", str(args.evidence_dir / "backup"), "--output", str(restore_dir),
                            "--api-port", str(args.restore_port), "--qdrant-port", str(args.restore_qdrant_port)],
                           check=True, stdout=log, stderr=log, timeout=900)
        restored = json.loads((restore_dir / "result.json").read_text())
        assert restored["database_equivalence_verified"]
        assert all(row["equivalent"] for row in restored["cold_file_equivalence"])
        result["checks"].append("all SQL table counts/checksums and cold storage file hashes match")
        base = restored["api_url"]
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            try:
                if request(base, "/health/ready")["dependencies"]["worker"] == "ok":
                    break
            except (OSError, KeyError):
                pass
            time.sleep(1)
        else:
            raise AssertionError("restored worker did not become ready")
        assert request(base, "/api/v1/model-budget")["mode"] == "fake"
        after = request(base, attempt_path)
        for field in ["answers", "reference_points", "index_id", "document_id", "feedback"]:
            assert before[field] == after[field], f"restored practice differs: {field}"
        assert after["attempt"]["status"] == "feedback_ready"
        result["checks"].append("frozen answers, five feedback/reference results and article identity survive restore")
        assert request(base, document_path)["active_index_id"] == document["active_index_id"]
        assert request(base, evidence_url) == historical
        result["checks"].append("active index and historical cited evidence survive restore")
        assert request(base, task_path)["report"]["markdown_content"] == learning_detail["report"]["markdown_content"]
        assert request(base, image_path)["status"] == "ready"
        with urllib.request.urlopen(base + image_path + "/content", timeout=30) as response:
            assert hashlib.sha256(response.read()).digest() == hashlib.sha256(image).digest()
        result["checks"].append("nonempty report export and original image snapshot survive restore")
        search = request(base, "/api/v1/rag/query", {"question": "事务失败如何回滚", "top_k": 5})
        assert any(c["document_id"] == args.document_id for c in search["citations"])
        result["checks"].append("restored Qdrant supplies citations to the retained article")
        created = request(base, f'/api/v1/question-sets/{before["attempt"]["question_set_id"]}/attempts', {})
        assert created["id"] != args.attempt_id
        assert request(base, f'/api/v1/practice-attempts/{created["id"]}')["answers"] == [""] * 5
        result["checks"].append("restored application creates an independent new practice")
        result["status"] = "passed"
        restored["business_restore_verified"] = True
        restored["status"] = "business_restore_verified_fake"
        (restore_dir / "result.json").write_text(json.dumps(restored, ensure_ascii=False, indent=2))
    except Exception as error:
        result["error"] = str(error)
        raise
    finally:
        # This configuration names only new volumes generated by the restore tool.
        if (restore_dir / "result.json").exists():
            restored = json.loads((restore_dir / "result.json").read_text())
            with (args.evidence_dir / "cleanup.log").open("w") as log:
                cleanup = subprocess.run(["docker", "compose", "-p", restored["project"], "-f",
                                          str(restore_dir.resolve() / "compose.json"), "down", "-v"],
                                         stdout=log, stderr=log, timeout=180)
            result["restore_cleanup_completed"] = cleanup.returncode == 0
            if cleanup.returncode:
                result["status"] = "failed"
                result["error"] = "isolated restore cleanup failed"
        (args.evidence_dir / "result.json").write_text(json.dumps(result, ensure_ascii=False, indent=2))
    if result["status"] != "passed":
        raise RuntimeError(result.get("error", "recovery acceptance failed"))
    print(f'PASS: {len(result["checks"])} full-stack recovery checks')


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--project", required=True)
    parser.add_argument("--document-id", type=int, required=True)
    parser.add_argument("--attempt-id", type=int, required=True)
    parser.add_argument("--chunk-id", required=True)
    parser.add_argument("--evidence-dir", type=Path, required=True)
    parser.add_argument("--restore-port", type=int, default=18085)
    parser.add_argument("--restore-qdrant-port", type=int, default=18086)
    args = parser.parse_args()
    url = urlparse(args.base_url)
    if url.scheme != "http" or url.hostname not in ("127.0.0.1", "localhost"):
        parser.error("local isolated API required")
    if not all(1024 <= p <= 65535 for p in [args.restore_port, args.restore_qdrant_port]) or args.restore_port == args.restore_qdrant_port:
        parser.error("distinct unprivileged restore ports required")
    run(args)
