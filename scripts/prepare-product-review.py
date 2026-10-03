#!/usr/bin/env python3
"""Freeze an existing private article and prepare unexecuted quality review.
No network calls, credentials, model calls, or inferred answer labels.
"""
import argparse
import hashlib
import json
from pathlib import Path


def prepare(article, expected_hash, output):
    raw = article.read_bytes()
    actual = hashlib.sha256(raw).hexdigest()
    if actual != expected_hash:
        raise ValueError("article hash differs from the frozen review input")
    source = raw.decode("utf-8")
    report = {
        "status": "prepared_not_executed", "article_sha256": actual,
        "article_bytes": len(raw), "article_lines": len(source.splitlines()),
        "external_model_calls": 0, "quality_validated": False,
        "budget_microcny": 0, "execution_authorized": False,
        "coverage_review": {"major_sections": [], "image_only_knowledge": [],
                            "five_connected_questions": None, "notes": None},
        "image_review": {"visible_nodes": [], "arrow_directions": [],
                         "visible_text": [], "uncertainty": [], "snapshot_hashes": []},
        "answer_cases": [{"category": category, "question_ordinal": None,
                          "answer": None, "expected_supported_facts": [],
                          "expected_omissions": [], "forbidden_accusations": [],
                          "output": None, "citation_support": None, "verdict": None}
                         for category in ["correct", "partially_correct", "incorrect",
                                          "questions_article", "unsupported_or_off_topic"]],
        "model_plan": {"text_model": None, "vision_model": None, "embedding_model": None,
                       "price_verified_at": None, "call_limit": None, "budget_approved": False},
        "execution": None, "user_acceptance": None,
    }
    # Avoid including the private source, answers or even file path in public fixtures.
    output.parent.mkdir(parents=True, exist_ok=True)
    with output.open("x", encoding="utf-8") as handle:
        handle.write(json.dumps(report, ensure_ascii=False, indent=2))
    output.chmod(0o600)
    return report


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--article", type=Path, required=True)
    parser.add_argument("--sha256", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    prepare(args.article, args.sha256, args.output)
    print(f"Prepared an unexecuted private review: {args.output}")
