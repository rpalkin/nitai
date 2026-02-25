FROM python:3.11-slim

WORKDIR /app

# Install dependencies first for better layer caching.
COPY pyproject.toml .
RUN pip install --no-cache-dir .

# Copy source after dependencies so code changes don't bust the dep layer.
COPY . .

# Indexer is a one-shot CLI tool — the command is provided at runtime:
#   kubectl run indexer --image=... -- python main.py /repo --qdrant-url http://qdrant:6333
# or triggered via Tilt local_resource / k8s Job.
CMD ["python", "main.py", "--help"]
