#!/usr/bin/env python3
"""Maintenance-mode cold backup of an explicitly selected Compose project."""
import argparse
import datetime as dt
import gzip
import importlib.util
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import threading

_db_spec = importlib.util.spec_from_file_location("stack_db_inventory", Path(__file__).with_name("stack-db-inventory.py"))
db_inventory = importlib.util.module_from_spec(_db_spec)
_db_spec.loader.exec_module(db_inventory)


def execute(command):
    return subprocess.run(command, check=True, capture_output=True, text=True, timeout=180).stdout.strip()


def stream_backup(command, destination):
    # Never collect documents or SQL in memory; gzip and hash on disk.
    with destination.open("xb") as output:
        with gzip.GzipFile(fileobj=output, mode="wb", mtime=0) as compressed:
            with tempfile.TemporaryFile() as stderr:
                with subprocess.Popen(command, stdout=subprocess.PIPE, stderr=stderr) as process:
                    deadline = threading.Timer(900, process.kill)
                    deadline.start()
                    try:
                        shutil.copyfileobj(process.stdout, compressed)
                        if process.wait() != 0:
                            # Configuration text and credentials are not logged.
                            raise RuntimeError(f"backup command failed ({process.returncode})")
                    except BaseException:
                        process.kill()
                        process.wait()
                        raise
                    finally:
                        deadline.cancel()
    digest = hashlib.sha256()
    with destination.open("rb") as source:
        for block in iter(lambda: source.read(1024*1024), b""):
            digest.update(block)
    return {"path": destination.name, "bytes": destination.stat().st_size, "sha256": digest.hexdigest()}


def backup(args):
    if not re.fullmatch(r"[a-z0-9][a-z0-9_-]{0,62}", args.project):
        raise ValueError("explicit Compose project name required")
    if not args.maintenance:
        raise ValueError("--maintenance required: backup temporarily stops API/worker/Qdrant/Redis")
    os.umask(0o077)
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    compose = ["docker", "compose", "--project-name", args.project]
    containers = {}
    stopped = []
    manifest = {"format": "learnq-cold-backup-v1", "status": "incomplete", "project": args.project, "created_at": dt.datetime.now(dt.timezone.utc).isoformat(), "components": [], "images": {}, "retention_days": args.retention_days, "expires_at": (dt.datetime.now(dt.timezone.utc)+dt.timedelta(days=args.retention_days)).isoformat(), "runtime_secrets_included": False, "restore_verified": False}
    try:
        for service in ["mysql", "redis", "qdrant", "api", "worker"]:
            ids = execute(compose + ["ps", "--all", "--quiet", service]).splitlines()
            if len(ids) != 1:
                raise RuntimeError(f"expected one existing {service} container")
            container = ids[0]
            state = json.loads(execute(["docker", "inspect", "--format", "{{json .State.Running}}", container]))
            image = execute(["docker", "inspect", "--format", "{{.Image}}", container])
            containers[service] = {"id": container, "running": state}
            manifest["images"][service] = image
        if not containers["mysql"]["running"]:
            raise RuntimeError("MySQL must already be running for a consistent logical dump")
        # Stop writers first, waiting for worker's normal shutdown grace.
        for service in ["api", "worker"]:
            if containers[service]["running"]:
                stopped.append(service)
                execute(["docker", "stop", "--time", "120", containers[service]["id"]])
        if containers["redis"]["running"]:
            execute(["docker", "exec", containers["redis"]["id"], "redis-cli", "SAVE"])
        for service in ["qdrant", "redis"]:
            if containers[service]["running"]:
                stopped.append(service)
                execute(["docker", "stop", "--time", "60", containers[service]["id"]])
        manifest["quiesced_at"] = dt.datetime.now(dt.timezone.utc).isoformat()
        command = ["docker", "exec", containers["mysql"]["id"], "sh", "-c", 'export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"; exec mysqldump -uroot --single-transaction --quick --hex-blob --no-tablespaces --set-gtid-purged=OFF "$MYSQL_DATABASE"']
        manifest["components"].append(stream_backup(command, output/"mysql.sql.gz"))
        for service, directory, filename in [
            ("qdrant", "/qdrant/storage/.", "qdrant-storage.tar.gz"),
            ("worker", "/app/data/images/.", "images.tar.gz"),
            ("worker", "/app/data/reports/.", "reports.tar.gz"),
            ("redis", "/data/.", "redis-data.tar.gz"),
        ]:
            command = ["docker", "cp", f"{containers[service]['id']}:{directory}", "-"]
            manifest["components"].append(stream_backup(command, output/filename))
        manifest["database_inventory"] = db_inventory.inventory(containers["mysql"]["id"])
        manifest["status"] = "complete_not_restored"
    except Exception as error:
        manifest["error"] = str(error)
        raise
    finally:
        restart_errors = []
        # Resume only containers that were running before this backup.
        for service in ["redis", "qdrant", "worker", "api"]:
            if service in stopped:
                try:
                    execute(["docker", "start", containers[service]["id"]])
                except Exception:
                    restart_errors.append(service)
        manifest["restart_errors"] = restart_errors
        manifest["previously_running_containers_restart_requested"] = not restart_errors
        manifest["post_backup_health_verified"] = False
        (output/"manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2)+"\n")
        if restart_errors:
            raise RuntimeError("backup completed or failed, but some containers did not restart: "+", ".join(restart_errors))
    print(f"Backup captured: {output}; restore remains unverified")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--project", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--maintenance", action="store_true")
    parser.add_argument("--retention-days", type=int, default=30)
    args = parser.parse_args()
    if not 1 <= args.retention_days <= 3650:
        parser.error("retention-days must be 1..3650")
    backup(args)
