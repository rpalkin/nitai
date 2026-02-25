#!/usr/bin/env bash
# End-to-end test for the ai-reviewer pipeline using ConnectRPC JSON API.
# Requires: Docker, Docker Compose, curl, jq
# Usage: ./tests/e2e.sh [--no-teardown]
#
# Required env vars (skip test if not set):
#   GITLAB_URL                 — e.g. https://gitlab.example.com
#   GITLAB_TOKEN               — personal access token with api scope
#   GITLAB_MR_IID              — merge request IID (project-scoped integer)
#   GITLAB_PROJECT_REMOTE_ID   — project remote ID (integer, from GitLab API)
#
# Pass --no-teardown to leave the stack running after the test.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

TEARDOWN=true
for arg in "$@"; do
  [[ "$arg" == "--no-teardown" ]] && TEARDOWN=false
done

# ── Check required env vars ────────────────────────────────────────────────────

MISSING=false
for var in GITLAB_URL GITLAB_TOKEN GITLAB_MR_IID GITLAB_PROJECT_REMOTE_ID; do
  if [[ -z "${!var:-}" ]]; then
    MISSING=true
  fi
done

if $MISSING; then
  echo "SKIP: e2e test requires GITLAB_URL, GITLAB_TOKEN, GITLAB_MR_IID, GITLAB_PROJECT_REMOTE_ID"
  echo "      Set these env vars and re-run to execute the full pipeline test."
  exit 0
fi

# ── Prerequisites ──────────────────────────────────────────────────────────────

echo "==> Checking prerequisites"
for cmd in docker curl jq; do
  if ! command -v "$cmd" &>/dev/null; then
    echo "ERROR: '$cmd' not found in PATH" >&2
    exit 1
  fi
done

if [[ ! -f "$REPO_ROOT/gen/go/go.mod" ]]; then
  echo "ERROR: gen/go/go.mod not found — run buf generate first" >&2
  exit 1
fi
echo "    OK"

API_BASE="http://localhost:8090"
RESTATE_ADMIN="http://localhost:9070"
REVIEW_TIMEOUT=300  # 5 minutes

cleanup() {
  if $TEARDOWN; then
    echo
    echo "==> Tearing down..."
    docker compose down --remove-orphans
  else
    echo
    echo "Stack left running (--no-teardown). Stop with: docker compose down"
  fi
}
trap cleanup EXIT

# ── Start stack ────────────────────────────────────────────────────────────────

echo "==> Starting full stack..."
docker compose --profile migrate run --rm migrate 2>/dev/null || true
docker compose up -d --build restate worker reviewer api-server restate-register
echo "    OK"

# ── Wait for Restate health ────────────────────────────────────────────────────

echo "==> Waiting for Restate admin API..."
for i in $(seq 1 30); do
  curl -sf "$RESTATE_ADMIN/health" &>/dev/null && break
  [[ $i -eq 30 ]] && { echo "ERROR: Restate admin API did not become ready" >&2; exit 1; }
  sleep 2
done
echo "    OK"

# ── Wait for restate-register ─────────────────────────────────────────────────

echo "==> Waiting for restate-register to complete..."
for i in $(seq 1 60); do
  STATUS=$(docker compose ps --all --format json restate-register 2>/dev/null | jq -r '.State // empty' 2>/dev/null || true)
  if [[ "$STATUS" == "exited" ]]; then
    EXIT_CODE=$(docker compose ps --all --format json restate-register 2>/dev/null | jq -r '.ExitCode // 1' 2>/dev/null || echo 1)
    if [[ "$EXIT_CODE" == "0" ]]; then
      echo "    OK"
      break
    else
      echo "ERROR: restate-register exited with code $EXIT_CODE" >&2
      docker compose logs restate-register >&2
      exit 1
    fi
  fi
  [[ $i -eq 60 ]] && { echo "ERROR: restate-register did not complete in time" >&2; exit 1; }
  sleep 2
done

# ── Wait for api-server ────────────────────────────────────────────────────────

echo "==> Waiting for api-server..."
for i in $(seq 1 30); do
  curl -sf "$API_BASE/healthz" &>/dev/null && break
  [[ $i -eq 30 ]] && { echo "ERROR: api-server did not become ready" >&2; exit 1; }
  sleep 2
done
echo "    OK"

# ── Helper: ConnectRPC call ────────────────────────────────────────────────────

connectrpc() {
  local service="$1"
  local method="$2"
  local body="$3"
  curl -sf \
    -X POST \
    -H "Content-Type: application/json" \
    "$API_BASE/$service/$method" \
    -d "$body"
}

# ── 1. CreateProvider ─────────────────────────────────────────────────────────

echo "==> CreateProvider"
PROVIDER_RESP=$(connectrpc "api.v1.ProviderService" "CreateProvider" \
  "$(jq -n \
    --arg name "e2e-test-provider" \
    --arg type "PROVIDER_TYPE_GITLAB_SELF_HOSTED" \
    --arg baseUrl "$GITLAB_URL" \
    --arg token "$GITLAB_TOKEN" \
    '{name: $name, type: $type, baseUrl: $baseUrl, token: $token}')")
PROVIDER_ID=$(echo "$PROVIDER_RESP" | jq -r '.provider.id')
echo "    Provider ID: $PROVIDER_ID"

# ── 2. ListRepos → find target repo ───────────────────────────────────────────

echo "==> ListRepos (finding repo with remote ID $GITLAB_PROJECT_REMOTE_ID)"
REPOS_RESP=$(connectrpc "api.v1.RepoService" "ListRepos" \
  "$(jq -n --arg providerId "$PROVIDER_ID" '{providerId: $providerId}')")
REPO_ID=$(echo "$REPOS_RESP" | jq -r \
  --argjson remoteId "$GITLAB_PROJECT_REMOTE_ID" \
  '.repositories[] | select(.remoteId == ($remoteId | tostring) or .remoteId == $remoteId) | .id' | head -1)
if [[ -z "$REPO_ID" ]]; then
  echo "ERROR: repo with remote ID $GITLAB_PROJECT_REMOTE_ID not found in provider" >&2
  echo "Available repos: $(echo "$REPOS_RESP" | jq -c '[.repositories[] | {id, remoteId, name}]')" >&2
  exit 1
fi
echo "    Repo ID: $REPO_ID"

# ── 3. EnableReview ───────────────────────────────────────────────────────────

echo "==> EnableReview"
connectrpc "api.v1.RepoService" "EnableReview" \
  "$(jq -n --arg repoId "$REPO_ID" '{repoId: $repoId}')" | jq -r '.repository.reviewEnabled' || true
echo "    OK"

# ── 4. TriggerReview ─────────────────────────────────────────────────────────

echo "==> TriggerReview (MR IID: $GITLAB_MR_IID)"
REVIEW_RESP=$(connectrpc "api.v1.ReviewService" "TriggerReview" \
  "$(jq -n \
    --arg repoId "$REPO_ID" \
    --argjson mrNumber "$GITLAB_MR_IID" \
    '{repoId: $repoId, mrNumber: $mrNumber}')")
RUN_ID=$(echo "$REVIEW_RESP" | jq -r '.reviewRun.id')
echo "    Review run ID: $RUN_ID"

# ── 5. Poll GetReviewRun until COMPLETED ──────────────────────────────────────

echo "==> Polling GetReviewRun (timeout: ${REVIEW_TIMEOUT}s)..."
DEADLINE=$((SECONDS + REVIEW_TIMEOUT))
COMPLETED=false
while [[ $SECONDS -lt $DEADLINE ]]; do
  RUN_RESP=$(connectrpc "api.v1.ReviewService" "GetReviewRun" \
    "$(jq -n --arg id "$RUN_ID" '{id: $id}')")
  STATUS=$(echo "$RUN_RESP" | jq -r '.reviewRun.status // empty')
  echo "    Status: $STATUS ($(( DEADLINE - SECONDS ))s remaining)"
  if [[ "$STATUS" == "REVIEW_STATUS_COMPLETED" ]]; then
    COMPLETED=true
    break
  elif [[ "$STATUS" == "REVIEW_STATUS_FAILED" ]]; then
    echo "ERROR: review run failed" >&2
    echo "$RUN_RESP" | jq . >&2
    exit 1
  fi
  sleep 10
done

if ! $COMPLETED; then
  echo "ERROR: review run did not complete within ${REVIEW_TIMEOUT}s" >&2
  exit 1
fi
echo "    Completed!"

# ── 6. Verify comments exist ──────────────────────────────────────────────────

echo "==> Verifying review comments"
COMMENT_COUNT=$(echo "$RUN_RESP" | jq '.reviewRun.comments | length')
if [[ "$COMMENT_COUNT" -eq 0 ]]; then
  echo "WARNING: review completed but no comments were posted" >&2
else
  echo "    $COMMENT_COUNT comment(s) posted"
fi

echo
echo "E2E test passed."
