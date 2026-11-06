"""Runs garak, then writes a short summary of its scores to the termination log.

Kubernetes copies a container's termination log into its pod's status, which
is how the operator reads the results without needing access to pod logs.
"""

import json
import subprocess
import sys
from pathlib import Path

REPORT = Path.home() / ".local/share/garak/garak_runs/run.report.jsonl"
TERMINATION_LOG = Path("/dev/termination-log")
# Kubernetes keeps at most 4096 bytes of a termination message.
MAX_BYTES = 4096


def main():
    garak = subprocess.run([sys.executable, "-m", "garak", "--report_prefix", "run", *sys.argv[1:]])
    if garak.returncode != 0:
        return garak.returncode

    results = []
    for line in REPORT.read_text().splitlines():
        entry = json.loads(line)
        if entry.get("entry_type") == "eval":
            results.append({
                "probe": entry["probe"],
                "detector": entry["detector"],
                "passed": entry["passed"],
                "total": entry["total_evaluated"],
            })

    summary = json.dumps({"results": results})
    if len(summary.encode()) > MAX_BYTES:
        print(f"the results summary is {len(summary.encode())} bytes, over the {MAX_BYTES}-byte limit", file=sys.stderr)
        return 1
    TERMINATION_LOG.write_text(summary)
    print(summary)
    return 0


if __name__ == "__main__":
    sys.exit(main())
