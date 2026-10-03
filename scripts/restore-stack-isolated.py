#!/usr/bin/env python3
"""Restore only into a generated Compose project with new volumes, in Fake mode."""
import argparse
import datetime as dt
import gzip
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import threading
import time
import urllib.request
import uuid

spec = importlib.util.spec_from_file_location("verify_backup", Path(__file__).with_name("verify-stack-backup.py"))
verifier = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verifier)

_db_spec = importlib.util.spec_from_file_location("stack_db_inventory", Path(__file__).with_name("stack-db-inventory.py"))
db_inventory = importlib.util.module_from_spec(_db_spec)
_db_spec.loader.exec_module(db_inventory)
_file_spec = importlib.util.spec_from_file_location("stack_file_inventory", Path(__file__).with_name("stack-file-inventory.py"))
file_inventory = importlib.util.module_from_spec(_file_spec)
_file_spec.loader.exec_module(file_inventory)



def command(args):
    return subprocess.run(args, check=True, capture_output=True, text=True, timeout=180).stdout.strip()


def feed(command_args, source):
    with tempfile.TemporaryFile() as stderr:
        with subprocess.Popen(command_args, stdin=subprocess.PIPE, stdout=subprocess.DEVNULL, stderr=stderr) as process:
            deadline = threading.Timer(600, process.kill)
            deadline.start()
            try:
                with gzip.open(source, "rb") as stream:
                    shutil.copyfileobj(stream, process.stdin)
                process.stdin.close()
                if process.wait(timeout=600) != 0:
                    raise RuntimeError("restore component command failed")
            except BaseException:
                process.kill()
                process.wait()
                raise
            finally:
                deadline.cancel()


def restore(args):
    integrity = verifier.verify(args.backup, args.max_unpacked_bytes)
    manifest = json.loads((args.backup/"manifest.json").read_text())
    images = manifest["images"]
    for service in ["mysql", "redis", "qdrant", "api", "worker"]:
        image = images[service]
        if not image.startswith("sha256:") or len(image) != 71:
            raise ValueError("backup image ID is invalid")
        command(["docker", "image", "inspect", "--format", "{{.Id}}", image])
    os.umask(0o077)
    args.output.mkdir(parents=True, exist_ok=False)
    project = "learnq_restore_"+uuid.uuid4().hex[:20]
    if command(["docker", "ps", "-aq", "--filter", f"label=com.docker.compose.project={project}"]):
        raise ValueError("random restore project collision")
    if command(["docker", "volume", "ls", "-q", "--filter", f"label=com.docker.compose.project={project}"]):
        raise ValueError("random restore volume collision")
    env = {"AI_MODE":"fake", "AI_DAILY_BUDGET_MICROCNY":"0", "AI_CALL_RESERVE_MICROCNY":"0", "MYSQL_DSN":"learnq:learnq@tcp(mysql:3306)/learnq?parseTime=true&charset=utf8mb4&multiStatements=true&timeout=5s&readTimeout=10s&writeTimeout=10s", "REDIS_ADDR":"redis:6379", "QDRANT_URL":"http://qdrant:6333", "HTTP_ADDR":"0.0.0.0:8080", "IMAGE_DIR":"/app/data/images", "REPORT_DIR":"/app/data/reports", "EMBEDDING_DIM":"1024", "DOCUMENT_CHUNK_VERSION":"markdown-block-800-v1"}
    services = {
        "mysql":{"image":images["mysql"], "environment":{"MYSQL_DATABASE":"learnq","MYSQL_USER":"learnq","MYSQL_PASSWORD":"learnq","MYSQL_ROOT_PASSWORD":"isolated-restore-only"},"volumes":["mysql:/var/lib/mysql"]},
        "redis":{"image":images["redis"],"volumes":["redis:/data"]},
        "qdrant":{"image":images["qdrant"],"volumes":["qdrant:/qdrant/storage"],"ports":[f"127.0.0.1:{args.qdrant_port}:6333"]},
        "api":{"image":images["api"],"environment":env,"volumes":["images:/app/data/images"],"ports":[f"127.0.0.1:{args.api_port}:8080"]},
        "worker":{"image":images["worker"],"environment":env,"volumes":["images:/app/data/images","reports:/app/data/reports"]},
    }
    compose_path = args.output.resolve()/"compose.json"
    compose_path.write_text(json.dumps({"services":services,"volumes":{name:{} for name in ["mysql","redis","qdrant","images","reports"]}},indent=2))
    compose = ["docker","compose","--project-name",project,"--file",str(compose_path)]
    result = {"status":"restore_incomplete","project":project,"backup_integrity":integrity,"mode":"fake","external_model_calls":0,"business_restore_verified":False,"created_at":dt.datetime.now(dt.timezone.utc).isoformat()}
    containers = {}
    try:
        command(compose+["create","--no-build","--pull","never"])
        for service in services:
            containers[service]=command(compose+["ps","--all","--quiet",service])
            if not containers[service]:
                raise RuntimeError("restore container missing")
        for service, path, filename in [("qdrant","/qdrant/storage","qdrant-storage.tar.gz"),("redis","/data","redis-data.tar.gz"),("worker","/app/data/images","images.tar.gz"),("worker","/app/data/reports","reports.tar.gz")]:
            feed(["docker","cp","--archive","-",f"{containers[service]}:{path}"],args.backup/filename)
        restored_files = []
        expected_files = {item["component"]: item["file_hashes"] for item in integrity["components"]}
        for service,path,filename in [("qdrant","/qdrant/storage","qdrant-storage.tar.gz"),("redis","/data","redis-data.tar.gz"),("worker","/app/data/images","images.tar.gz"),("worker","/app/data/reports","reports.tar.gz")]:
            actual_files = file_inventory.inventory(containers[service],path,args.max_unpacked_bytes)
            if actual_files != sorted(expected_files[filename],key=lambda item:item["path"]):
                raise ValueError("restored file contents differ: "+filename)
            restored_files.append({"component":filename,"files":len(actual_files),"equivalent":True})
        result["cold_file_equivalence"] = restored_files
        command(compose+["start","mysql"])
        for _ in range(90):
            try:
                command(["docker","exec",containers["mysql"],"sh","-c",'export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"; exec mysql -h127.0.0.1 -uroot -e "SELECT 1"'])
                break
            except subprocess.CalledProcessError:
                time.sleep(2)
        else:
            raise RuntimeError("restore MySQL did not initialize")
        feed(["docker","exec","-i",containers["mysql"],"sh","-c",'export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"; exec mysql -uroot "$MYSQL_DATABASE"'],args.backup/"mysql.sql.gz")
        result["sql_import_completed"] = True
        actual = db_inventory.inventory(containers["mysql"])
        result["restored_database_inventory"] = actual
        if "database_inventory" not in manifest:
            raise ValueError("backup has no frozen database inventory; equivalence cannot be proven")
        if actual != manifest["database_inventory"]:
            raise ValueError("restored database counts/checksums differ from backup")
        result["database_equivalence_verified"] = True
        command(compose+["start","redis","qdrant","api"])
        for _ in range(90):
            try:
                with urllib.request.urlopen(f"http://127.0.0.1:{args.api_port}/health/live",timeout=3) as response:
                    if response.status == 200:
                        break
            except OSError:
                pass
            time.sleep(2)
        else:
            raise RuntimeError("restored API did not start")
        command(compose+["start","worker"])
        result["status"] = "restored_requires_business_verification"
        result["api_url"] = f"http://127.0.0.1:{args.api_port}"
        result["limitations"] = ["SQL counts/checksums and cold file hashes match; API liveness alone does not prove business recovery", "Fake mode prevents paid model requests; original real-mode vector quality is not tested", "random project and volumes are retained for inspection; no automatic removal"]
    except Exception as error:
        result["error"] = str(error)
        raise
    finally:
        (args.output/"result.json").write_text(json.dumps(result,ensure_ascii=False,indent=2)+"\n")
    print(json.dumps(result,ensure_ascii=False,indent=2))


if __name__ == "__main__":
    parser=argparse.ArgumentParser()
    parser.add_argument("--backup",type=Path,required=True)
    parser.add_argument("--output",type=Path,required=True)
    parser.add_argument("--api-port",type=int,default=18083)
    parser.add_argument("--qdrant-port",type=int,default=18084)
    parser.add_argument("--max-unpacked-bytes",type=int,default=10*1024**3)
    args=parser.parse_args()
    if not all(1024 <= p <= 65535 for p in [args.api_port,args.qdrant_port]) or args.api_port==args.qdrant_port:
        parser.error("distinct unprivileged API and Qdrant ports required")
    restore(args)
