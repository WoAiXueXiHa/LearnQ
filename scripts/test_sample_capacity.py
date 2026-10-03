import importlib.util
import contextlib
import io
import json
import subprocess
import sys
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

    def test_transient_docker_error_records_gap_and_recovers(self):
        output = io.StringIO()
        error = subprocess.CalledProcessError(1, ["docker", "stats"])
        with mock.patch.object(sys, "argv", ["sampler", "--project", "isolated", "--samples", "2"]):
            with mock.patch.object(capacity, "sample", side_effect=[error, {"project": "isolated", "containers": []}]):
                with mock.patch.object(capacity.time, "sleep"), contextlib.redirect_stdout(output):
                    self.assertEqual(capacity.main(), 0)
        rows = [json.loads(line) for line in output.getvalue().splitlines()]
        self.assertEqual(rows[0]["sample_error"], "CalledProcessError")
        self.assertNotIn("sample_error", rows[1])

    def test_persistent_docker_error_is_bounded(self):
        error = subprocess.CalledProcessError(1, ["docker", "stats"])
        with mock.patch.object(sys, "argv", ["sampler", "--project", "isolated", "--samples", "10", "--max-consecutive-errors", "2"]):
            with mock.patch.object(capacity, "sample", side_effect=error) as sample:
                with mock.patch.object(capacity.time, "sleep"), contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                    self.assertEqual(capacity.main(), 1)
        self.assertEqual(sample.call_count, 2)

    def test_final_failed_sample_does_not_report_success(self):
        error = subprocess.CalledProcessError(1, ["docker", "stats"])
        for outcomes in ([error], [{"project": "isolated"}, error]):
            with self.subTest(samples=len(outcomes)):
                with mock.patch.object(sys, "argv", ["sampler", "--project", "isolated", "--samples", str(len(outcomes))]):
                    with mock.patch.object(capacity, "sample", side_effect=outcomes):
                        with mock.patch.object(capacity.time, "sleep"), contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                            self.assertEqual(capacity.main(), 1)


if __name__ == "__main__":
    unittest.main()
