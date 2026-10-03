"""Inventory restored container files without extracting them on the host."""
import hashlib
from pathlib import PurePosixPath
import subprocess
import tarfile
import threading


def inventory(container, directory, max_bytes):
    entries = []
    total = 0
    with subprocess.Popen(["docker","cp",f"{container}:{directory}/.","-"], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL) as process:
        deadline = threading.Timer(180, process.kill)
        deadline.start()
        try:
            with tarfile.open(fileobj=process.stdout, mode="r|") as archive:
                for member in archive:
                    if not member.isfile():
                        continue
                    total += member.size
                    if total > max_bytes:
                        raise ValueError("restored file inventory exceeds limit")
                    stream = archive.extractfile(member)
                    digest = hashlib.sha256()
                    for block in iter(lambda: stream.read(1024*1024), b""):
                        digest.update(block)
                    entries.append({"path":str(PurePosixPath(member.name)),"bytes":member.size,"sha256":digest.hexdigest()})
            # Docker must finish successfully after tar's end marker.
            while process.stdout.read(1024*1024):
                pass
            if process.wait(timeout=180) != 0:
                raise RuntimeError("restored file inventory failed")
        except BaseException:
            process.kill()
            process.wait()
            raise
        finally:
            deadline.cancel()
    return sorted(entries,key=lambda item:item["path"])
