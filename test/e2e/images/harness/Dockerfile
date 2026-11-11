# Stand-in for the garak harness in e2e tests. It has Python, which the
# operator's wait-for-target init container needs, and a run.py that reports
# a fixed result the same way the real harness does.
FROM python:3.12-slim

COPY run.py /opt/harness/run.py

# A numeric non-root user, so the image meets the restricted Pod Security level.
USER 1000:1000
ENTRYPOINT ["python", "/opt/harness/run.py"]
