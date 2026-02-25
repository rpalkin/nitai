FROM python:3.12-slim

WORKDIR /app

# Install dependencies first for layer caching
COPY pyproject.toml .
RUN pip install --no-cache-dir .

# Copy source
COPY reviewer/ reviewer/

CMD ["python", "-m", "reviewer.service"]
