# Red team harness: garak, installed at build time so a run never needs PyPI.
FROM python:3.12-slim

# garak needs PyTorch. Installing the CPU build first keeps pip from pulling
# in several GB of GPU libraries.
RUN pip install --no-cache-dir torch --index-url https://download.pytorch.org/whl/cpu \
    && pip install --no-cache-dir garak==0.17.1

# Run as a normal user. garak writes its reports under this user's home directory.
# The numeric ID lets Kubernetes check that the user isn't root.
RUN useradd --create-home --uid 1000 garak
USER 1000
WORKDIR /home/garak

# run.py runs garak and hands its scores back to the operator.
COPY run.py /opt/harness/run.py
ENTRYPOINT ["python", "/opt/harness/run.py"]
