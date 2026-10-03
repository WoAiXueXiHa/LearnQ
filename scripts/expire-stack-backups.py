#!/usr/bin/env python3
"""Preview expiry; apply only a saved, unchanged plan for recognized backups."""
import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import shutil

PAYLOADS = {"mysql.sql.gz", "qdrant-storage.tar.gz", "images.tar.gz", "reports.tar.gz", "redis-data.tar.gz"}


def fingerprint(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024*1024), b""):
            digest.update(block)
    return digest.hexdigest()


def plan(root, now=None):
    if root.is_symlink() or not root.is_dir():
        raise ValueError("backup root must be a real directory")
    root = root.resolve()
    now = now or dt.datetime.now(dt.timezone.utc)
    candidates = []
    ignored = []
    for directory in sorted(root.iterdir()):
        if directory.is_symlink() or not directory.is_dir():
            continue
        try:
            metadata = directory/"manifest.json"
            if metadata.is_symlink():
                raise ValueError("manifest is a link")
            manifest = json.loads(metadata.read_text())
            if manifest.get("format") != "learnq-cold-backup-v1" or manifest.get("status") != "complete_not_restored":
                raise ValueError("not a complete recognized backup")
            expires = dt.datetime.fromisoformat(manifest["expires_at"])
            if expires.tzinfo is None:
                raise ValueError("expiry needs an explicit timezone")
            if expires > now:
                continue
            entries = {p.name for p in directory.iterdir()}
            if entries != PAYLOADS | {"manifest.json"}:
                raise ValueError("directory contains unexpected files")
            if {c["path"] for c in manifest["components"]} != PAYLOADS or len(manifest["components"]) != len(PAYLOADS):
                raise ValueError("backup component ledger is invalid")
            files = []
            for item in manifest["components"]:
                path = directory/item["path"]
                if path.is_symlink() or not path.is_file() or path.stat().st_size != item["bytes"] or fingerprint(path) != item["sha256"]:
                    raise ValueError("backup contents changed or invalid")
                files.append({"path":path.name,"bytes":item["bytes"],"sha256":item["sha256"]})
            stat = directory.stat()
            candidates.append({"directory":directory.name,"device":stat.st_dev,"inode":stat.st_ino,"expires_at":manifest["expires_at"],"manifest_sha256":fingerprint(metadata),"files":sorted(files,key=lambda f:f["path"])})
        except (OSError,ValueError,KeyError,TypeError) as error:
            ignored.append({"directory":directory.name,"reason":str(error)})
    payload = {"format":"learnq-backup-expiry-plan-v1","root":str(root),"candidates":candidates}
    payload["plan_hash"] = hashlib.sha256(json.dumps(payload,sort_keys=True,separators=(",",":")).encode()).hexdigest()
    payload["ignored"] = ignored
    return payload


def apply(saved):
    current = plan(Path(saved["root"]))
    if current["plan_hash"] != saved["plan_hash"] or current["candidates"] != saved["candidates"]:
        raise ValueError("expiry impact changed; create and review a new plan")
    deleted = []
    for candidate in current["candidates"]:
        directory = Path(current["root"])/candidate["directory"]
        stat = directory.stat(follow_symlinks=False)
        if directory.is_symlink() or (stat.st_dev,stat.st_ino)!=(candidate["device"],candidate["inode"]):
            raise ValueError("backup directory identity changed")
        shutil.rmtree(directory)
        deleted.append(candidate["directory"])
    return {"status":"expired_backups_removed","root":current["root"],"plan_hash":saved["plan_hash"],"deleted":deleted}


if __name__ == "__main__":
    parser=argparse.ArgumentParser()
    parser.add_argument("--root",type=Path)
    parser.add_argument("--output",type=Path)
    parser.add_argument("--apply-plan",type=Path)
    args=parser.parse_args()
    if args.apply_plan:
        if args.root or args.output:
            parser.error("apply-plan cannot be combined with root/output")
        result=apply(json.loads(args.apply_plan.read_text()))
    else:
        if not args.root:
            parser.error("root required for preview")
        result=plan(args.root)
    if args.output:
        os.umask(0o077)
        with args.output.open("x") as stream:
            stream.write(json.dumps(result,ensure_ascii=False,indent=2)+"\n")
    print(json.dumps(result,ensure_ascii=False,indent=2))
