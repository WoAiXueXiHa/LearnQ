"""Public, synthetic regression tests; no private article or model calls."""
import importlib.util
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("eval_redis_qdrant", Path(__file__).with_name("eval-redis-qdrant.py"))
tool = importlib.util.module_from_spec(spec)
spec.loader.exec_module(tool)

class QdrantEvidenceToolTests(unittest.TestCase):
    def fixture(self):
        return {"article_sha256": "a" * 64, "questions": [{"id": "q1", "question": "机制是什么？", "evidence": [{"point": "完整步骤", "lines": [[2, 3], [8, 9]]}]}]}

    def test_build_keeps_frozen_spans_and_document_scope(self):
        fixture = self.fixture()
        cases = tool.build_cases(fixture, 9)
        self.assertEqual(cases[0]["document_id"], 9)
        self.assertEqual(cases[0]["article_sha256"], fixture["article_sha256"])
        self.assertEqual(cases[0]["evidence_points"], fixture["questions"][0]["evidence"])
        self.assertNotIn("relevant_chunks", cases[0])
        for invalid in (0, -1):
            with self.assertRaises(ValueError):
                tool.build_cases(fixture, invalid)

    def test_default_resolves_without_running_embeddings(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = Path(directory) / "fixture.json"
            fixture.write_text(json.dumps(self.fixture()), encoding="utf-8")
            output = Path(directory) / "report"
            args = ["eval-redis-qdrant.py", "--document-id", "9", "--fixture", str(fixture), "--output-dir", str(output)]
            with patch.object(sys, "argv", args), patch.object(tool, "request", return_value=json.dumps({"data":[{"id":"q1","relevant_chunks":[{"chunk_id":"c","relevance":3}],"evidence_points":[{"point":"完整步骤","chunk_ids":["c"]}]}]}).encode()) as request:
                tool.main()
            self.assertEqual(request.call_count, 1)
            self.assertTrue(request.call_args.args[0].endswith("?resolve_only=true"))
            run = next(output.iterdir())
            self.assertTrue((run / "dataset.jsonl").exists())
            self.assertEqual(json.loads((run / "run-status.json").read_text())["status"], "resolved")
            self.assertFalse((run / "report.md").exists())

    def test_resolution_failure_does_not_write_success_artifacts(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = Path(directory) / "fixture.json"
            fixture.write_text(json.dumps(self.fixture()), encoding="utf-8")
            output = Path(directory) / "report"
            args = ["eval-redis-qdrant.py", "--document-id", "9", "--fixture", str(fixture), "--output-dir", str(output)]
            with patch.object(sys, "argv", args), patch.object(tool, "request", side_effect=ValueError("hash mismatch")):
                with self.assertRaises(ValueError):
                    tool.main()
            run = next(output.iterdir())
            self.assertEqual(json.loads((run / "run-status.json").read_text())["status"], "failed")
            self.assertFalse((run / "report.md").exists())

    def test_failed_rerun_preserves_previous_success(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = Path(directory) / "fixture.json"
            fixture.write_text(json.dumps(self.fixture()), encoding="utf-8")
            output = Path(directory) / "report"
            args = ["tool", "--document-id", "9", "--fixture", str(fixture), "--output-dir", str(output), "--run"]
            resolved = json.dumps({"data":[{"id":"q1", "relevant_chunks":[{"chunk_id":"c"}], "evidence_points":[{"point":"完整步骤"}]}]}).encode()
            with patch.object(sys, "argv", args), patch.object(tool, "request", side_effect=[resolved, b'{"data":{"id":1,"mode":"pipeline_test"}}', b"original report"]):
                tool.main()
            previous = next(output.iterdir())
            with patch.object(sys, "argv", args), patch.object(tool, "request", return_value=b'{"data":[]}'):
                with self.assertRaises(ValueError):
                    tool.main()
            runs = list(output.iterdir())
            self.assertEqual(len(runs), 2)
            self.assertEqual((previous / "report.md").read_bytes(), b"original report")
            statuses = [json.loads((run / "run-status.json").read_text())["status"] for run in runs]
            self.assertCountEqual(statuses, ["succeeded", "failed"])

if __name__ == "__main__":
    unittest.main()
