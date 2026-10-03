#!/usr/bin/env python3
"""Prepare a hash-checked manual worksheet; does not run or score models."""
import argparse
import datetime as dt
import hashlib
import json
from pathlib import Path


def prepare(root, output):
    manifest_path = root / "manifest.json"
    manifest = json.loads(manifest_path.read_text())
    registered = {item["path"] for item in manifest["files"]}
    if len(registered) != len(manifest["files"]) or "cases.jsonl" not in registered:
        raise ValueError("manifest needs unique files and the cases ledger")
    for item in manifest["files"]:
        path = root / item["path"]
        if root.resolve() not in path.resolve().parents:
            raise ValueError("fixture path escapes dataset")
        if hashlib.sha256(path.read_bytes()).hexdigest() != item["sha256"]:
            raise ValueError(f"frozen hash mismatch: {item['path']}")
    cases = [json.loads(line) for line in (root / "cases.jsonl").read_text().splitlines() if line.strip()]
    seen = set()
    worksheet = []
    for case in cases:
        if case["id"] in seen or case["split"] not in ("development", "acceptance"):
            raise ValueError("duplicate case or invalid split")
        seen.add(case["id"])
        if case["article"] not in registered:
            raise ValueError(f"article is not frozen in manifest: {case['id']}")
        article = (root / case["article"]).read_bytes()
        if hashlib.sha256(article).hexdigest() != case["article_sha256"]:
            raise ValueError(f"case article hash mismatch: {case['id']}")
        lines = article.decode().splitlines()
        for evidence in case["evidence"]:
            start, end = evidence["lines"]
            if start < 1 or end < start or end > len(lines) or evidence["quote"] not in "\n".join(lines[start-1:end]):
                raise ValueError(f"invalid source evidence: {case['id']}")
        if not case["expectations"] or not case["forbidden_conclusions"]:
            raise ValueError(f"case needs explicit judging criteria: {case['id']}")
        worksheet.append({"input": case, "execution": None, "model_calls": [], "output": None, "review": {"verdict": None, "citation_support": None, "false_accusation": None, "uncertainty_handling": None, "notes": None}})
    if {case["split"] for case in cases} != {"development", "acceptance"}:
        raise ValueError("development and acceptance splits both required")
    development = {case["article"] for case in cases if case["split"] == "development"}
    acceptance = {case["article"] for case in cases if case["split"] == "acceptance"}
    if development & acceptance:
        raise ValueError("article cannot be reused across development and acceptance")
    report = {"status": "prepared_not_executed", "dataset_id": manifest["dataset_id"], "manifest_sha256": hashlib.sha256(manifest_path.read_bytes()).hexdigest(), "prepared_at": dt.datetime.now(dt.timezone.utc).isoformat(), "external_model_calls": 0, "quality_validated": False, "ready_for_full_quality_acceptance": not manifest["remaining_fixtures"], "cases": worksheet, "remaining_fixtures": manifest["remaining_fixtures"]}
    output.parent.mkdir(parents=True, exist_ok=True)
    with output.open("x") as handle:
        handle.write(json.dumps(report, ensure_ascii=False, indent=2))
    output.chmod(0o600)
    print(f"Prepared {len(cases)} unexecuted cases: {output}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--dataset", type=Path, default=Path("data/eval/practice"))
    parser.add_argument("--output", type=Path, default=Path("data/reports/practice-eval") / (dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%S%fZ") + ".json"))
    args = parser.parse_args()
    prepare(args.dataset, args.output)
