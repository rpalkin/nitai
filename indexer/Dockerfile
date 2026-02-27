FROM python:3.12-slim

RUN apt-get update && apt-get install -y --no-install-recommends git && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Install dependencies first for better layer caching.
COPY pyproject.toml .
RUN pip install --no-cache-dir .

# Copy source after dependencies so code changes don't bust the dep layer.
COPY indexer/ indexer/

CMD ["python", "-m", "indexer.service"]
