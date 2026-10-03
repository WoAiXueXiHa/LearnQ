import gzip
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("backup_verifier", Path(__file__).with_name("verify-stack-backup.py"))
verifier = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verifier)


class BackupValidationTests(unittest.TestCase):
    def fixture(self, root, entry="safe.txt", link=False):
        components = []
        for name in sorted(verifier.COMPONENTS):
            path = root/name
            if name.endswith(".tar.gz"):
                with tarfile.open(path, "w:gz") as archive:
                    member = tarfile.TarInfo(entry)
                    if link:
                        member.type = tarfile.SYMTYPE
                        member.linkname = "/outside"
                        archive.addfile(member)
                    else:
                        member.size = 3
                        archive.addfile(member, io.BytesIO(b"abc"))
            else:
                path.write_bytes(gzip.compress(b"CREATE TABLE synthetic(id INT);"))
            components.append({"path": name, "bytes": path.stat().st_size, "sha256": hashlib.sha256(path.read_bytes()).hexdigest()})
        manifest = {"format": "learnq-cold-backup-v1", "status": "complete_not_restored", "project": "synthetic", "components": components}
        (root/"manifest.json").write_text(json.dumps(manifest))

    def test_complete_integrity_is_not_restore_success(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.fixture(root)
            result = verifier.verify(root)
            self.assertEqual(result["status"], "integrity_checked_not_restored")
            self.assertFalse(result["restore_verified"])

    def test_path_escape_and_links_rejected(self):
        for entry, link in [("../outside", False), ("/outside", False), ("safe-link", True)]:
            with self.subTest(entry=entry), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                self.fixture(root, entry, link)
                with self.assertRaises(ValueError):
                    verifier.verify(root)

    def test_corruption_and_expansion_limit(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.fixture(root)
            with self.assertRaises(ValueError):
                verifier.verify(root, max_unpacked_bytes=1)
            path = root/"mysql.sql.gz"
            path.write_bytes(path.read_bytes()+b"corrupt")
            with self.assertRaises(ValueError):
                verifier.verify(root)

    def test_incomplete_backup_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.fixture(root)
            path = root/"manifest.json"
            manifest = json.loads(path.read_text())
            manifest["status"] = "incomplete"
            path.write_text(json.dumps(manifest))
            with self.assertRaises(ValueError):
                verifier.verify(root)
