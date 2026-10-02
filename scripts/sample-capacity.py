#!/usr/bin/env python3
"""Read-only Compose resource samples. Run on the target Linux server.

Does not start, stop, restart, inspect environment variables, or remove services.
Output is JSONL; a successful run is collection evidence, not a capacity verdict.
"""

import argparse
import datetime
import json
import re
import shutil
import subprocess
import sys
import time
from pathlib import Path


def docker(*args):
    result = subprocess.run(["docker", *args], check=True, text=True,
                            capture_output=True, timeout=30)
    return result.stdout


def counters(path, separator=None):
    result = {}
    for line in Path(path).read_text().splitlines():
        key, value = line.split(separator, 1) if separator else line.split(None, 1)
        result[key] = int(value.strip().split()[0])
    return result


def sample(project, disk_path):
    ids = docker("ps", "-aq", "--filter",
                 f"label=com.docker.compose.project={project}").split()
    if not ids:
        raise ValueError(f"no containers found for Compose project {project}")
    stats = docker("stats", "--no-stream", "--format", "{{json .}}", *ids)
    # Restrict inspect output to state and identity. Never emit Config.Env.
    states = docker("inspect", "--format",
                    '{{json .Name}} {{json .State}} {{json .RestartCount}}', *ids)
    state_rows = []
    decoder = json.JSONDecoder()
    for line in states.splitlines():
        values = []
        rest = line
        while rest.strip():
            value, end = decoder.raw_decode(rest.lstrip())
            values.append(value)
            rest = rest.lstrip()[end:]
        name, state, restarts = values
        state_rows.append({"name": name, "status": state["Status"],
                           "oom_killed": state["OOMKilled"],
                           "restart_count": restarts,
                           "exit_code": state["ExitCode"]})
    memory = counters("/proc/meminfo", ":")
    vm = counters("/proc/vmstat")
    disk = shutil.disk_usage(disk_path)
    return {
        "time_utc": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "project": project,
        "containers": [json.loads(line) for line in stats.splitlines() if line],
        "states": state_rows,
        "host_memory_kib": {key: memory[key] for key in
                            ("MemTotal", "MemAvailable", "SwapTotal", "SwapFree")},
        "host_swap_pages": {key: vm[key] for key in ("pswpin", "pswpout")},
        "disk_bytes": {"path": str(disk_path), "total": disk.total,
                       "used": disk.used, "free": disk.free},
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--project", required=True, help="explicit isolated Compose project")
    parser.add_argument("--samples", type=int, default=60)
    parser.add_argument("--interval", type=float, default=5)
    parser.add_argument("--disk-path", type=Path, default=Path("."),
                        help="path on the filesystem holding Docker data/volumes")
    args = parser.parse_args()
    if not re.fullmatch(r"[a-z0-9][a-z0-9_-]*", args.project):
        parser.error("invalid Compose project name")
    if args.samples < 1 or not 0 < args.interval <= 60:
        parser.error("samples must be positive; interval must be in (0, 60]")
    for index in range(args.samples):
        try:
            print(json.dumps(sample(args.project, args.disk_path)), flush=True)
        except (ValueError, OSError, subprocess.SubprocessError, KeyError) as error:
            # Docker error stderr can contain server details; keep output bounded.
            print(f"capacity sample failed: {type(error).__name__}: {error}", file=sys.stderr)
            return 1
        if index + 1 < args.samples:
            time.sleep(args.interval)
    return 0


if __name__ == "__main__":
    sys.exit(main())
