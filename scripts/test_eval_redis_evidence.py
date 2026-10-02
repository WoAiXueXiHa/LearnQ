"""Offline contract tests for the frozen Redis evidence evaluation.

Run with REDIS_ARTICLE_PATH pointing to the private article whose SHA-256 is
recorded in data/eval/redis_persistence_evidence.json. No API is called.
"""

import contextlib
import hashlib
import importlib.util
import io
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).with_name("eval-redis-evidence.py")
spec = importlib.util.spec_from_file_location("eval_redis_evidence", SCRIPT)
evaluation = importlib.util.module_from_spec(spec)
spec.loader.exec_module(evaluation)


class FrozenRedisEvaluationTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path = os.environ.get("REDIS_ARTICLE_PATH")
        if not path:
            raise unittest.SkipTest("set REDIS_ARTICLE_PATH to the private frozen article")
        cls.article = Path(path)
        cls.fixture = json.loads(evaluation.FIXTURE.read_text(encoding="utf-8"))
        actual = hashlib.sha256(cls.article.read_bytes()).hexdigest()
        if actual != cls.fixture["article_sha256"]:
            raise AssertionError(f"test article hash mismatch: {actual}")

    def run_main(self, article):
        output = io.StringIO()
        errors = io.StringIO()
        with mock.patch.object(sys, "argv", [str(SCRIPT), "--article", str(article), "--top-k", "2"]):
            with contextlib.redirect_stdout(output), contextlib.redirect_stderr(errors):
                evaluation.main()
        return output.getvalue(), errors.getvalue()

    def test_wrong_article_is_rejected_before_embedding_or_network(self):
        with tempfile.TemporaryDirectory() as directory:
            wrong = Path(directory) / "wrong.md"
            wrong.write_bytes(self.article.read_bytes() + b"\nwrong version")
            with mock.patch.object(evaluation, "embed") as embed:
                with mock.patch.object(evaluation.urllib.request, "urlopen") as network:
                    with self.assertRaises(SystemExit) as raised:
                        self.run_main(wrong)
            self.assertEqual(raised.exception.code, 2)
            embed.assert_not_called()
            network.assert_not_called()

    def test_hand_chosen_vectors_report_exact_hits_and_misses(self):
        chunks = evaluation.chunks_from_markdown(self.article.read_text(encoding="utf-8-sig"))
        questions = self.fixture["questions"]
        self.assertEqual(len(questions), 5)
        self.assertEqual(len(chunks), 60)

        # These ranks are chosen by hand to test scoring and reporting only.
        # They are not a measured dense or hybrid retrieval result.
        choices = {
            "redis-1": ((242, 243), (11, 12)),
            "redis-2": ((60, 60), (44, 44)),
            "redis-3": ((70, 73), (75, 75)),
            "redis-4": ((143, 143), (91, 91)),
            "redis-5": ((208, 209), (215, 218)),
        }
        expected_coverage = {
            "redis-1": "4/4",
            "redis-2": "1/3",
            "redis-3": "3/3",
            "redis-4": "1/3",
            "redis-5": "3/4",
        }
        self.assertEqual(set(choices), {question["id"] for question in questions})
        targets = []
        for question in questions:
            positions = []
            for start, end in choices[question["id"]]:
                matching = [index for index, chunk in enumerate(chunks)
                            if chunk["start"] == start and chunk["end"] == end]
                self.assertEqual(len(matching), 1, (question["id"], start, end))
                positions.append(matching[0])
            targets.append(positions)

        def fake_embed(texts, account, token):
            self.assertEqual(len(texts), len(chunks) + len(questions))
            vectors = [[float(column == index) for column in range(len(chunks))]
                       for index in range(len(chunks))]
            for top_one, top_two in targets:
                query = [0.0] * len(chunks)
                query[top_one] = 1.0
                query[top_two] = 0.5
                vectors.append(query)
            return vectors

        with mock.patch.dict(os.environ, {"CF_ACCOUNT_ID": "mock", "CF_API_TOKEN": "mock"}):
            with mock.patch.object(evaluation, "embed", side_effect=fake_embed) as embed:
                with mock.patch.object(evaluation.urllib.request, "urlopen") as network:
                    output, errors = self.run_main(self.article)

        self.assertEqual(errors, "")
        self.assertEqual(embed.call_count, 1)
        network.assert_not_called()
        self.assertEqual(output.count("证据点覆盖："), 5)
        for question in questions:
            heading = f"{question['id']}：{question['question']}"
            self.assertIn(heading, output)
            section = output.split(heading, 1)[1].split("\nredis-", 1)[0]
            self.assertIn(f"证据点覆盖：{expected_coverage[question['id']]}", section)
            start, end = choices[question["id"]][0]
            self.assertIn(f"Top1: 第{start}-{end}行", section)
            start, end = choices[question["id"]][1]
            self.assertIn(f"Top2: 第{start}-{end}行", section)

        redis_one = output.split("redis-1：", 1)[1].split("\nredis-2：", 1)[0]
        self.assertIn("命中 RDB 保存时间点快照", redis_one)
        self.assertIn("命中 AOF 记录写命令", redis_one)
        for question_id, absent in (
            ("redis-2", "父进程继续处理请求，子进程生成快照"),
            ("redis-4", "把增量追加到新 AOF 再替换"),
        ):
            section = output.split(f"{question_id}：", 1)[1].split("\nredis-", 1)[0]
            self.assertIn(f"缺失 {absent}", section)


if __name__ == "__main__":
    unittest.main()
