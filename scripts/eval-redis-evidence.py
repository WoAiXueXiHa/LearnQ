#!/usr/bin/env python3
"""Evaluate evidence coverage of a Redis article with Cloudflare embeddings.

Uses only the Python standard library. Run --dry-run first to inspect chunks.
The live mode sends article chunks and five questions to Cloudflare Workers AI.
"""

import argparse
import hashlib
import json
import math
import os
import re
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path


MODEL = "@cf/qwen/qwen3-embedding-0.6b"
FIXTURE = Path(__file__).resolve().parent.parent / "data/eval/redis_persistence_evidence.json"


def chunks_from_markdown(article, limit=400):
    chunks = []
    heading = ""
    block = []
    start = 0
    in_fence = False

    def flush():
        nonlocal block, start
        if not block:
            return
        content = "\n".join(line for _, line in block)
        if re.fullmatch(r"(?:先建立一个全局认知|具体的流程是|常见的方式|和混合持久化做个对比|官方提供了三种刷盘策略)[：:]?", content.strip()):
            block = []
            start = 0
            return
        chunks.append({"start": start, "end": block[-1][0], "heading": heading, "content": content})
        block = []
        start = 0

    for number, line in enumerate(article.splitlines(), 1):
        stripped = line.strip()
        if stripped.lstrip("> ").startswith("```"):
            flush()
            in_fence = not in_fence
            continue
        if in_fence or stripped.startswith("![") or stripped.startswith("> !["):
            flush()
            continue
        if re.match(r"^#{1,6} ", stripped):
            flush()
            heading = f"{stripped} (第 {number} 行)"
            continue
        if not stripped:
            flush()
            continue
        if not block:
            start = number
        if block and sum(len(part) for _, part in block) + len(line) > limit:
            flush()
            start = number
        block.append((number, line))
    flush()
    return chunks


def embed(texts, account, token):
    url = f"https://api.cloudflare.com/client/v4/accounts/{account}/ai/v1/embeddings"
    vectors = []
    batch_count = (len(texts) + 4) // 5
    for offset in range(0, len(texts), 5):
        batch = offset // 5 + 1
        started = time.monotonic()
        print(f"Cloudflare embedding：第 {batch}/{batch_count} 批，请求中…", file=sys.stderr, flush=True)
        payload = json.dumps({"model": MODEL, "input": texts[offset:offset + 5]}).encode()
        request = urllib.request.Request(url, payload, {
            "Authorization": f"Bearer {token}", "Content-Type": "application/json"
        })
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                data = json.load(response)
        except urllib.error.HTTPError as exc:
            raise RuntimeError(f"第 {batch}/{batch_count} 批：Cloudflare HTTP {exc.code}") from exc
        except (urllib.error.URLError, TimeoutError) as exc:
            raise RuntimeError(f"第 {batch}/{batch_count} 批网络请求失败：{exc}") from exc
        print(f"Cloudflare embedding：第 {batch}/{batch_count} 批完成，耗时 {time.monotonic() - started:.1f}s", file=sys.stderr, flush=True)
        data = data.get("result", data)
        items = sorted(data["data"], key=lambda item: item["index"])
        if len(items) != len(texts[offset:offset + 5]):
            raise RuntimeError("embedding response count does not match input count")
        vectors.extend(item["embedding"] for item in items)
    if any(len(vector) != 1024 for vector in vectors):
        raise RuntimeError("expected 1024-dimensional vectors")
    return vectors


def cosine(a, b):
    numerator = sum(x * y for x, y in zip(a, b))
    denominator = math.sqrt(sum(x * x for x in a) * sum(y * y for y in b))
    return numerator / denominator if denominator else 0.0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--article", type=Path, required=True)
    parser.add_argument("--dry-run", action="store_true", help="only list chunks; no network calls")
    parser.add_argument("--top-k", type=int, default=5)
    args = parser.parse_args()
    if args.top_k < 1:
        parser.error("--top-k must be positive")
    article = args.article.read_text(encoding="utf-8-sig")
    article_hash = hashlib.sha256(args.article.read_bytes()).hexdigest()
    chunks = chunks_from_markdown(article)
    fixture = json.loads(FIXTURE.read_text(encoding="utf-8"))
    expected_hash = fixture.get("article_sha256")
    if expected_hash and article_hash != expected_hash:
        parser.error(f"article SHA-256 mismatch: expected {expected_hash}, got {article_hash}")
    print(f"文章 SHA-256：{article_hash}；评测集 SHA-256：{hashlib.sha256(FIXTURE.read_bytes()).hexdigest()}", flush=True)
    print(f"文章行数：{len(article.splitlines())}；候选片段：{len(chunks)}；模型：{MODEL}；chunker：markdown-block-v2", flush=True)
    if args.dry_run:
        for index, chunk in enumerate(chunks, 1):
            print(f"{index:02d}. 第{chunk['start']}-{chunk['end']}行 {chunk['heading']}: {chunk['content'][:70]}")
        return
    account = os.environ.get("CF_ACCOUNT_ID")
    token = os.environ.get("CF_API_TOKEN")
    if not account or not token:
        parser.error("set CF_ACCOUNT_ID and CF_API_TOKEN for live evaluation")
    texts = [f"{chunk['heading']}\n{chunk['content']}" for chunk in chunks]
    questions = fixture["questions"]
    vectors = embed(texts + [q["question"] for q in questions], account, token)
    chunk_vectors = vectors[:len(chunks)]
    for question, query_vector in zip(questions, vectors[len(chunks):]):
        order = sorted(range(len(chunks)), key=lambda i: cosine(query_vector, chunk_vectors[i]), reverse=True)
        top = [chunks[i] for i in order[:args.top_k]]
        print(f"\n{question['id']}：{question['question']}")
        found = 0
        for point in question["evidence"]:
            hit = any(chunk["start"] <= start and chunk["end"] >= end
                      for chunk in top for start, end in point["lines"])
            found += hit
            print(f"  {'命中' if hit else '缺失'} {point['point']}")
        print(f"  证据点覆盖：{found}/{len(question['evidence'])}")
        for rank, chunk in enumerate(top, 1):
            print(f"  Top{rank}: 第{chunk['start']}-{chunk['end']}行 {chunk['heading']} | {chunk['content'][:90]}")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError, RuntimeError) as exc:
        print(f"评测失败：{exc}", file=sys.stderr)
        sys.exit(1)
