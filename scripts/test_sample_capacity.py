import importlib.util
import json
import unittest
from pathlib import Path
from unittest import mock


spec = importlib.util.spec_from_file_location("sample_capacity", Path(__file__).with_name("sample-capacity.py"))
capacity = importlib.util.module_from_spec(spec)
spec.loader.exec_module(capacity)


class CapacitySampleTest(unittest.TestCase):
    def test_collects_state_without_dumping_environment(self):
        state = {"Status": "running", "OOMKilled": False, "ExitCode": 0}
        outputs = ["abc\n", '{"MemUsage":"25MiB / 2GiB"}\n',
                   '"/api" ' + json.dumps(state) + ' 2\n']
        with mock.patch.object(capacity, "docker", side_effect=outputs) as docker:
            with mock.patch.object(capacity, "counters", side_effect=[
                {"MemTotal": 2000, "MemAvailable": 1000, "SwapTotal": 500, "SwapFree": 400},
                {"pswpin": 10, "pswpout": 20},
            ]):
                result = capacity.sample("isolated", Path("."))
        self.assertEqual(result["states"][0]["restart_count"], 2)
        self.assertEqual(result["host_swap_pages"]["pswpout"], 20)
        self.assertIn("label=com.docker.compose.project=isolated", docker.call_args_list[0].args)
        self.assertNotIn("Config", str(docker.call_args_list))
        self.assertTrue(all(call.args[0] in ("ps", "stats", "inspect") for call in docker.call_args_list))

    def test_missing_project_fails_before_stats(self):
        with mock.patch.object(capacity, "docker", return_value="") as docker:
            with self.assertRaisesRegex(ValueError, "no containers"):
                capacity.sample("absent", Path("."))
        self.assertEqual(docker.call_count, 1)


if __name__ == "__main__":
    unittest.main()
