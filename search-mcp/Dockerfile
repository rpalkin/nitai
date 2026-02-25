FROM python:3.11-slim

WORKDIR /app

# Install dependencies first for better layer caching.
COPY pyproject.toml .
RUN pip install --no-cache-dir .

# Copy source after dependencies so code changes don't bust the dep layer.
COPY . .

# NOTE: server.py calls mcp.run() which defaults to stdio transport.
# For in-cluster deployment you need HTTP transport. Pass transport via env:
#   MCP_TRANSPORT=streamable-http MCP_HOST=0.0.0.0 MCP_PORT=8080
# FastMCP reads these env vars automatically (fastmcp >= 2.x).
# See: https://gofastmcp.com/deployment/running-server
CMD ["python", "server.py"]

EXPOSE 8080
