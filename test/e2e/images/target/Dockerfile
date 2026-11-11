# Stand-in for the target model in e2e tests. It answers HTTP on Ollama's
# port, which is all the operator and the stand-in harness check for.
FROM python:3.12-slim

# An empty directory, so the server has nothing to hand out.
RUN mkdir /srv/empty
WORKDIR /srv/empty

# A numeric non-root user, so the image meets the restricted Pod Security level.
USER 1000:1000
CMD ["python", "-m", "http.server", "11434"]
