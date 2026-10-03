#!/usr/bin/env python3
"""Validate backup integrity and extraction safety; never restore or start services."""
import argparse
import gzip
import hashlib
import json
from pathlib import Path, PurePosixPath
import tarfile

COMPONENTS = {"mysql.sql.gz", "qdrant-storage.tar.gz", "images.tar.gz", "reports.tar.gz", "redis-data.tar.gz"}


def verify(root, max_unpacked_bytes=10*1024**3):
    root = root.resolve()
    manifest = json.loads((root/"manifest.json").read_text())
    if manifest.get("format") != "learnq-cold-backup-v1" or manifest.get("status") != "complete_not_restored":
        raise ValueError("backup is incomplete or uses an unsupported format")
    components = manifest["components"]
    if len(components) != len(COMPONENTS) or {c["path"] for c in components} != COMPONENTS:
        raise ValueError("required components missing or duplicated")
    unpacked = 0
    gzip_bytes = 0
    inventory = []
    for component in components:
        path = root/component["path"]
        if path.is_symlink() or not path.is_file() or path.resolve().parent != root:
            raise ValueError("component is not a local regular file")
        if path.stat().st_size != component["bytes"]:
            raise ValueError(f"size mismatch: {path.name}")
        digest = hashlib.sha256()
        with path.open("rb") as source:
            for block in iter(lambda: source.read(1024*1024), b""):
                digest.update(block)
        if digest.hexdigest() != component["sha256"]:
            raise ValueError(f"hash mismatch: {path.name}")
        count = 0
        file_hashes = []
        if path.name.endswith(".tar.gz"):
            seen = set()
            with tarfile.open(path, "r|gz") as archive:
                for member in archive:
                    name = PurePosixPath(member.name)
                    if name.is_absolute() or ".." in name.parts or "\\" in member.name or not (member.isfile() or member.isdir()):
                        raise ValueError(f"unsafe archive entry: {path.name}")
                    normalized = str(name)
                    if normalized in seen:
                        raise ValueError(f"duplicate archive entry: {path.name}")
                    seen.add(normalized)
                    if member.isfile():
                        count += 1
                        if member.size < 0 or unpacked+member.size > max_unpacked_bytes:
                            raise ValueError("unpacked backup exceeds configured limit")
                        stream = archive.extractfile(member)
                        remaining = member.size
                        file_digest = hashlib.sha256()
                        while remaining:
                            data = stream.read(min(1024*1024, remaining))
                            if not data:
                                raise ValueError(f"truncated archive entry: {path.name}")
                            file_digest.update(data)
                            remaining -= len(data)
                        file_hashes.append({"path":normalized,"bytes":member.size,"sha256":file_digest.hexdigest()})
                        unpacked += member.size
        else:
            with gzip.open(path, "rb") as source:
                for block in iter(lambda: source.read(1024*1024), b""):
                    unpacked += len(block)
                    if unpacked > max_unpacked_bytes:
                        raise ValueError("unpacked backup exceeds configured limit")
        # Consume the gzip trailer as well; tar's end marker alone is insufficient.
        with gzip.open(path, "rb") as source:
            for block in iter(lambda: source.read(1024*1024), b""):
                gzip_bytes += len(block)
                if gzip_bytes > max_unpacked_bytes + 16*1024**2:
                    raise ValueError("archive overhead or trailing payload exceeds limit")
        inventory.append({"component": path.name, "files": count, "file_hashes": file_hashes, "sha256": component["sha256"]})
    return {"status": "integrity_checked_not_restored", "backup_project": manifest["project"], "components": inventory, "unpacked_bytes": unpacked, "restore_verified": False}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--backup", type=Path, required=True)
    parser.add_argument("--max-unpacked-bytes", type=int, default=10*1024**3)
    args = parser.parse_args()
    if args.max_unpacked_bytes < 1:
        parser.error("max-unpacked-bytes must be positive")
    print(json.dumps(verify(args.backup, args.max_unpacked_bytes), ensure_ascii=False, indent=2))
