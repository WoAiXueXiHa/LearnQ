#!/usr/bin/env python3
"""Synthetic Fake API acceptance; records evidence, never uses a paid model."""
import argparse
import datetime as dt
import hashlib
import io
import json
import subprocess
import sys
from pathlib import Path
import time
import urllib.error
import urllib.request
import zipfile


def poll_read(fetch,path,ready,failed=lambda value: False,seconds=120,expected=(200,),on_gap=lambda error: None,clock=None,sleep=None):
    """Bounded GET polling; retry transport gaps, never application errors/writes."""
    clock = clock or time.monotonic
    sleep = sleep or time.sleep
    deadline = clock() + seconds
    while clock() < deadline:
        try:
            value = fetch(path,expected=expected,timeout=max(1,min(10,deadline-clock())))
        except OSError as error:
            on_gap(error)
            sleep(1)
            continue
        if ready(value):
            return value
        assert not failed(value), f"terminal failure: {path}: {value}"
        sleep(1)
    raise AssertionError(f"timeout waiting for {path}")


def run(args):
    root = Path(args.evidence_dir)
    root.mkdir(parents=True, exist_ok=True)
    events = []
    checks = []

    def request(path, method="GET", body=None, expected=(200,), raw=False, timeout=60):
        payload = None if body is None else json.dumps(body).encode()
        headers = {} if payload is None else {"Content-Type": "application/json"}
        req = urllib.request.Request(args.base_url + path, data=payload, headers=headers, method=method)
        try:
            with urllib.request.urlopen(req, timeout=timeout) as response:
                status, content = response.status, response.read()
        except urllib.error.HTTPError as error:
            status, content = error.code, error.read()
        except OSError as error:
            events.append({"method":method,"path":path,"status":None,"transport_error":type(error).__name__})
            (root / "requests.json").write_text(json.dumps(events,ensure_ascii=False,indent=2))
            raise
        value = None if raw else json.loads(content)
        events.append({"method": method, "path": path, "status": status, "response": value if not raw else {"bytes": len(content), "sha256": hashlib.sha256(content).hexdigest()}})
        (root / "requests.json").write_text(json.dumps(events, ensure_ascii=False, indent=2))
        assert status in expected, f"{method} {path}: HTTP {status}, {content[:1000]!r}"
        return content if raw else value.get("data")

    def check(name, condition):
        assert condition, name
        checks.append(name)

    def poll(path, ready, failed=lambda value: False, seconds=120, expected=(200,)):
        return poll_read(request,path,ready,failed,seconds,expected,
                         on_gap=lambda error: print(f"Read-only poll transport gap at {path}: {type(error).__name__}; retrying within deadline",file=sys.stderr))

    try:
        for _ in range(90):
            try:
                ready = request("/health/ready")
                if ready["dependencies"]["worker"] == "ok":
                    break
            except (OSError, AssertionError, ValueError):
                pass
            time.sleep(2)
        else:
            raise AssertionError("isolated stack did not become ready")
        budget = request("/api/v1/model-budget")
        check("Fake mode and zero external budget", budget["mode"] == "fake" and budget["daily_budget_microcny"] == 0)
        source = """# 合成事务文章

## 业务问题
订单创建需要同时写订单表与库存表，部分成功会造成不一致。

## 原子性
事务将多次写入作为一个整体，失败时回滚全部未提交修改。

## 并发
对同一库存行加锁，提交前再次检查库存，避免库存被扣成负数。

## 恢复
进程失败后由数据库回滚未提交事务，已提交结果保留。

## 边界
数据库事务不能直接保证外部通知发送成功，通知需要独立重试与幂等。

![受限地址测试](http://127.0.0.1:9/private.png)
""".encode()
        (root / "synthetic.md").write_bytes(source)
        boundary = "LearnQPracticeFixture"
        upload = b"--" + boundary.encode() + b'\r\nContent-Disposition: form-data; name="file"; filename="synthetic.md"\r\nContent-Type: text/markdown\r\n\r\n' + source + b"\r\n--" + boundary.encode() + b"--\r\n"
        req = urllib.request.Request(args.base_url + "/api/v1/documents", upload, {"Content-Type": "multipart/form-data; boundary=" + boundary})
        with urllib.request.urlopen(req, timeout=30) as response:
            document_id = json.load(response)["data"]["document_id"]
        doc = poll(f"/api/v1/documents/{document_id}/status", lambda v: v["status"] == "ready", lambda v: v["status"] == "failed")
        index_id = doc["active_index_id"]
        image_path = f"/api/v1/documents/{document_id}/indexes/{index_id}/images"
        images = request(image_path)
        check("unprocessed image blocks evidence completeness", len(images["images"]) == 1 and not images["image_evidence_complete"])
        request(f"/api/v1/documents/{document_id}/question-sets", "POST", expected=(409,))
        image = images["images"][0]
        request(f'{image_path}/{image["id"]}/process', "POST", expected=(422,))
        failed_images = request(image_path)
        check("unsafe remote image fails explicitly and keeps evidence incomplete", not failed_images["image_evidence_complete"] and failed_images["images"][0]["status"] == "download_failed")
        request(f'{image_path}/{image["id"]}/skip', "POST", {"reason": "合成安全失败样例，明确跳过以验证文字闭环"})
        check("explicit skip completes evidence", request(image_path)["image_evidence_complete"])
        created = request(f"/api/v1/documents/{document_id}/question-sets", "POST", expected=(202,))
        set_id = created["id"]
        draft = poll(f"/api/v1/question-sets/{set_id}", lambda v: v["question_set"]["status"] == "draft", lambda v: v["question_set"]["status"] == "failed")
        questions = draft["questions"]
        check("exactly five private draft questions", len(questions) == 5 and all("reference_points" not in q and "reference_items" not in q for q in questions))
        original_prompt = questions[0]["prompt"]
        questions[0]["prompt"] += "（用户编辑）"
        request(f"/api/v1/question-sets/{set_id}", "PUT", {"questions": questions})
        check("draft edit persisted", request(f"/api/v1/question-sets/{set_id}")["questions"][0]["prompt"] != original_prompt)
        request(f"/api/v1/question-sets/{set_id}/confirm", "POST", {"questions": questions})
        request(f"/api/v1/question-sets/{set_id}", "PUT", {"questions": questions}, expected=(409,))
        attempt = request(f"/api/v1/question-sets/{set_id}/attempts", "POST", expected=(201,))
        attempt_id = attempt["id"]
        attempt_path = f"/api/v1/practice-attempts/{attempt_id}"
        hidden = request(attempt_path)
        private_export=request(f"/api/v1/documents/{document_id}/export", raw=True)
        with zipfile.ZipFile(io.BytesIO(private_export)) as bundle:
            exported_questions=[json.loads(line) for line in bundle.read("questions.jsonl").decode().splitlines()]
            exported_edits=[json.loads(line) for line in bundle.read("question_edits.jsonl").decode().splitlines()]
            check("unsubmitted export hides legacy and structured reference points", all(row["reference_points_json"]=="[]" and row["reference_items_json"]=="null" for row in exported_questions) and all(not q.get("reference_items") and not q.get("reference_points") for row in exported_edits for q in json.loads(row["questions_json"])))
        check("references and feedback hidden before submission", all(key not in hidden for key in ["reference_points","reference_items","reference_evidence","reference_metadata","feedback"]))
        partial = ["已保存草稿", "", "", "", ""]
        request(attempt_path + "/answers", "PUT", {"answers": partial})
        check("draft survives reload", request(attempt_path)["answers"] == partial)
        request(attempt_path + "/submit", "POST", {"answers": partial}, expected=(409,))
        answers = [f"第 {n+1} 题合成回答：事务、回滚、锁与外部通知边界。" for n in range(5)]
        request(attempt_path + "/submit", "POST", {"answers": answers})
        request(attempt_path + "/submit", "POST", {"answers": answers}, expected=(409,))
        request(attempt_path + "/answers", "PUT", {"answers": partial}, expected=(409,))
        result = poll(attempt_path, lambda v: v["attempt"]["status"] == "feedback_ready", lambda v: v["attempt"]["status"] == "feedback_failed")
        check("five feedback results with frozen answers", len(result["feedback"]) == 5 and len(result["reference_points"]) == 5 and result["answers"] == answers)
        feedback_id = result["feedback"][0]["feedback"]["id"]
        correction_path = f"/api/v1/answer-feedback/{feedback_id}/corrections"
        request(correction_path, "POST", {"disposition": "disputed", "comment": "合成质疑：检验复习有效性拦截"}, expected=(201,))
        due = (dt.datetime.now(dt.timezone.utc) + dt.timedelta(days=1)).isoformat()
        request(attempt_path + "/review", "POST", {"due_at": due}, expected=(409,))
        request(correction_path, "POST", {"disposition": "accepted", "comment": "仅接受 Fake 流程样例，不认领模型质量"}, expected=(201,))
        check("correction history retained", len(request(correction_path)) == 2)
        review = request(attempt_path + "/review", "POST", {"due_at": due})
        repeated = request(f'/api/v1/practice-reviews/{review["id"]}/start', "POST", expected=(201,))
        again = request(f'/api/v1/practice-reviews/{review["id"]}/start', "POST", expected=(201,))
        check("review start idempotent and independent", repeated["id"] == again["id"] and repeated["id"] != attempt_id)
        check("new attempt has no old answers", request(f'/api/v1/practice-attempts/{repeated["id"]}')["answers"] == [""] * 5)
        request(f"/api/v1/documents/{document_id}/archive", "POST")
        check("archived history remains readable", request(attempt_path)["answers"] == answers)
        request(f"/api/v1/documents/{document_id}/restore", "POST")
        request(f"/api/v1/documents/{document_id}/reindex", "POST", expected=(202,))
        rebuilt = poll(f"/api/v1/documents/{document_id}/status", lambda v: v["status"] == "ready" and v["active_index_id"] != index_id, lambda v: v["status"] == "failed")
        check("rebuild retains attempt's immutable evidence identity", request(attempt_path)["index_id"] == index_id and rebuilt["active_index_id"] != index_id)
        old_chunk = questions[0]["chunk_ids"][0]
        old_evidence = request(f"/api/v1/documents/{document_id}/indexes/{index_id}/chunks/{old_chunk}")
        check("historical cited chunk remains readable", bool(old_evidence))
        archive = request(f"/api/v1/documents/{document_id}/export", raw=True)
        (root / "export.zip").write_bytes(archive)
        with zipfile.ZipFile(io.BytesIO(archive)) as exported:
            check("export is valid and contains manifest", exported.testzip() is None and "manifest.json" in exported.namelist())
        ledger = request("/api/v1/model-calls")
        check("all recorded model calls are Fake", bool(ledger["calls"]) and all(call["mode"] == "fake" and call["reserved_microcny"] == 0 for call in ledger["calls"]))
        if args.backup_restore:
            subprocess.run([sys.executable, str(Path(__file__).with_name("acceptance-practice-recovery.py")),
                            "--base-url", args.base_url, "--project", args.project,
                            "--document-id", str(document_id), "--attempt-id", str(attempt_id),
                            "--chunk-id", old_chunk, "--evidence-dir", str(root / "recovery")], check=True)
            check("full-stack backup and business restore", True)
        plan = request(f"/api/v1/documents/{document_id}/deletion-plan")
        request(f"/api/v1/documents/{document_id}/purge", "POST", {"plan_hash": plan["plan_hash"], "confirmation": "invalid"}, expected=(422,))
        request(f"/api/v1/documents/{document_id}/purge", "POST", {"plan_hash": plan["plan_hash"], "confirmation": plan["required_confirmation"]}, expected=(202,))
        poll(f"/api/v1/documents/{document_id}/deletion", lambda v: v["status"] == "complete", seconds=180)
        request(f"/api/v1/documents/{document_id}/status", expected=(404,))
        request(attempt_path, expected=(404,))
        check("confirmed deletion reaches complete and removes practice history", all(row["id"] != set_id for row in request("/api/v1/question-sets")))
        report = {"status": "passed", "project": args.project, "checks": checks, "external_model_calls": 0, "quality_validated": False, "browser_validated": False, "article_sha256": hashlib.sha256(source).hexdigest()}
    except Exception as error:
        report = {"status": "failed", "project": args.project, "checks": checks, "error": str(error), "quality_validated": False}
        raise
    finally:
        (root / "result.json").write_text(json.dumps(report, ensure_ascii=False, indent=2))
    print(f"PASS: {len(checks)} checks; Fake API workflow only; evidence: {root}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--evidence-dir", required=True)
    parser.add_argument("--project", required=True)
    parser.add_argument("--backup-restore", action="store_true")
    run(parser.parse_args())
