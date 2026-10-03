"""Read only table counts and checksums through an existing MySQL container."""
import re
import subprocess


def inventory(container):
    def query(sql):
        result = subprocess.run(["docker", "exec", container, "sh", "-c", 'export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"; exec mysql -uroot --batch --skip-column-names "$MYSQL_DATABASE" -e "$1"', "inventory", sql], check=True, capture_output=True, text=True, timeout=300)
        return result.stdout.strip()
    tables = query("SHOW TABLES").splitlines()
    rows = []
    for table in tables:
        if not re.fullmatch(r"[A-Za-z0-9_]+", table):
            raise ValueError("unsupported table identifier")
        count = int(query(f"SELECT COUNT(*) FROM `{table}`"))
        checksum = query(f"CHECKSUM TABLE `{table}` EXTENDED").split("\t")[-1]
        if not checksum.isdecimal():
            raise ValueError(f"checksum unavailable: {table}")
        rows.append({"table": table, "rows": count, "checksum_extended": checksum})
    return sorted(rows, key=lambda row: row["table"])
