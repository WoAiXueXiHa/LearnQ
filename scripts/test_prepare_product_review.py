import importlib.util
from pathlib import Path
import tempfile
import hashlib
import unittest

spec = importlib.util.spec_from_file_location("product_review", Path(__file__).with_name("prepare-product-review.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

class ProductReviewPreparationTests(unittest.TestCase):
    def test_freezes_bytes_without_copying_private_content_or_authorizing_calls(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            article = root / "private.md"
            raw = "# 私有文章\r\n用户原文不进入评测模板。\r\n".encode()
            article.write_bytes(raw)
            output = root / "review.json"
            report = module.prepare(article, hashlib.sha256(raw).hexdigest(), output)
            self.assertEqual(article.read_bytes(), raw)
            self.assertEqual(len(report["answer_cases"]), 5)
            self.assertEqual(report["external_model_calls"], 0)
            self.assertFalse(report["execution_authorized"])
            self.assertNotIn("用户原文", output.read_text())
            with self.assertRaises(FileExistsError):
                module.prepare(article, hashlib.sha256(raw).hexdigest(), output)

    def test_mismatching_frozen_hash_leaves_no_report(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            article = root / "private.md"
            article.write_bytes(b"changed")
            output = root / "review.json"
            with self.assertRaises(ValueError):
                module.prepare(article, "0" * 64, output)
            self.assertFalse(output.exists())
