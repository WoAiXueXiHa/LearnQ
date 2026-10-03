import importlib.util
from pathlib import Path
import unittest
from unittest.mock import Mock

spec=importlib.util.spec_from_file_location("practice_acceptance",Path(__file__).with_name("acceptance-practice.py"))
module=importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

class Clock:
    def __init__(self):self.now=0
    def read(self):return self.now
    def sleep(self,seconds):self.now+=seconds

class PracticeReadPollingTests(unittest.TestCase):
    def test_read_transport_gap_is_recorded_then_recovers(self):
        clock=Clock()
        fetch=Mock(side_effect=[ConnectionResetError("controlled disconnect"),{"status":"ready"}])
        gaps=[]
        result=module.poll_read(fetch,"/status",lambda row:row["status"]=="ready",seconds=3,on_gap=gaps.append,clock=clock.read,sleep=clock.sleep)
        self.assertEqual(result,{"status":"ready"})
        self.assertEqual(len(gaps),1)
        self.assertEqual(fetch.call_count,2)
        self.assertEqual(fetch.call_args.args,("/status",))
        self.assertLessEqual(fetch.call_args.kwargs["timeout"],10)

    def test_bad_application_result_is_not_retried(self):
        for result in [AssertionError("HTTP 500"),{"status":"failed"}]:
            with self.subTest(result=result):
                clock=Clock()
                fetch=Mock(side_effect=[result])
                with self.assertRaises(AssertionError):
                    module.poll_read(fetch,"/status",lambda row:False,failed=lambda row:row["status"]=="failed",clock=clock.read,sleep=clock.sleep)
                self.assertEqual(fetch.call_count,1)

    def test_persistent_disconnect_hits_deadline(self):
        clock=Clock()
        fetch=Mock(side_effect=ConnectionResetError("controlled disconnect"))
        with self.assertRaisesRegex(AssertionError,"timeout"):
            module.poll_read(fetch,"/status",lambda row:False,seconds=3,clock=clock.read,sleep=clock.sleep)
        self.assertEqual(fetch.call_count,3)
