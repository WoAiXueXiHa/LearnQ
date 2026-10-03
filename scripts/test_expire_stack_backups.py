import datetime as dt
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

from scripts import test_verify_stack_backup as validation_fixtures

spec=importlib.util.spec_from_file_location("backup_expiry",Path(__file__).with_name("expire-stack-backups.py"))
expiry=importlib.util.module_from_spec(spec)
spec.loader.exec_module(expiry)


class BackupExpiryTests(unittest.TestCase):
    def fixture(self, root, name, expired):
        directory=root/name
        directory.mkdir()
        validation_fixtures.BackupValidationTests().fixture(directory)
        path=directory/"manifest.json"
        manifest=json.loads(path.read_text())
        manifest["expires_at"]=(dt.datetime.now(dt.timezone.utc)+dt.timedelta(days=-1 if expired else 1)).isoformat()
        path.write_text(json.dumps(manifest))
        return directory

    def test_preview_then_explicit_apply_only_expired_recognized_backup(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary)
            old=self.fixture(root,"old",True)
            fresh=self.fixture(root,"fresh",False)
            unknown=root/"unrelated"
            unknown.mkdir()
            (unknown/"important.txt").write_text("preserve")
            planned=expiry.plan(root)
            self.assertEqual([c["directory"] for c in planned["candidates"]],["old"])
            self.assertTrue(old.exists())
            result=expiry.apply(planned)
            self.assertEqual(result["deleted"],["old"])
            self.assertTrue(fresh.exists())
            self.assertTrue((unknown/"important.txt").exists())

    def test_changed_backup_cannot_be_deleted_with_old_plan(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary)
            old=self.fixture(root,"old",True)
            planned=expiry.plan(root)
            (old/"new-user-file").write_text("preserve")
            with self.assertRaises(ValueError):
                expiry.apply(planned)
            self.assertTrue(old.exists())

    def test_backup_symlink_is_excluded(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary)
            old=self.fixture(root,"old",True)
            (root/"linked").symlink_to(old,target_is_directory=True)
            self.assertEqual([c["directory"] for c in expiry.plan(root)["candidates"]],["old"])
