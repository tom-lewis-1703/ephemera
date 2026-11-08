# Target model for red team runs: Ollama with the model weights built in,
# so a sandbox never needs to download anything.
FROM ollama/ollama:0.40.15

# Listen on every interface so garak can reach the model from another pod.
# Keep the model and Ollama's own files where a non-root user can write them.
ENV OLLAMA_HOST=0.0.0.0:11434 \
    OLLAMA_MODELS=/models \
    HOME=/home/ollama

ARG MODEL=qwen2.5:0.5b

# Run as a non-root user, so the image meets the restricted Pod Security level.
RUN mkdir -p /models /home/ollama && chown 1000:1000 /models /home/ollama
USER 1000:1000

# Pulling a model needs the Ollama server running, so start it for this
# step only, wait until it answers, pull the model, then stop it.
RUN ollama serve & server=$! \
    && until ollama list >/dev/null 2>&1; do sleep 1; done \
    && ollama pull "$MODEL" \
    && kill "$server"
