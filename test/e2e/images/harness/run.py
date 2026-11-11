"""Stands in for garak in e2e tests.

It makes one request to the target through its Service, then writes a fixed
summary to the termination log, in the same format as images/harness/run.py.
The request fails the Job if the harness can't reach the target.
"""

import json
import urllib.request
from pathlib import Path

urllib.request.urlopen("http://target:11434/", timeout=10)

summary = {"results": [{"probe": "e2e.Stub", "detector": "e2e.Reached", "passed": 1, "total": 1}]}
Path("/dev/termination-log").write_text(json.dumps(summary))
