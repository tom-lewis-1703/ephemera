# Target model for red team runs: Ollama with the model weights built in,
# so a sandbox never needs to download anything.
FROM ollama/ollama:0.40.15

# Listen on every interface so garak can reach the model from another pod.
ENV OLLAMA_HOST=0.0.0.0:11434

ARG MODEL=qwen2.5:0.5b

# Pulling a model needs the Ollama server running, so start it for this
# step only, wait until it answers, pull the model, then stop it.
RUN ollama serve & server=$! \
    && until ollama list >/dev/null 2>&1; do sleep 1; done \
    && ollama pull "$MODEL" \
    && kill "$server"
