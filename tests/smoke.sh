#!/usr/bin/env bash
# Smoke tests for the ai-reviewer stack: migrations, service startup, Restate auto-registration.
# Requires: Docker, Docker Compose, curl, jq
# Usage: ./tests/smoke.sh [--no-teardown]
#
# By default, docker compose services are stopped after the test.
# Pass --no-teardown to leave them running for manual inspection.
#
# Registration is handled automatically by the restate-register init container.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

TEARDOWN=true
for arg in "$@"; do
  [[ "$arg" == "--no-teardown" ]] && TEARDOWN=false
done

RESTATE_ADMIN="http://localhost:9070"

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

# ── Prerequisites ──────────────────────────────────────────────────────────────

echo "==> Checking prerequisites"
for cmd in docker curl jq; do
  if ! command -v "$cmd" &>/dev/null; then
    echo "ERROR: '$cmd' not found in PATH" >&2
    exit 1
  fi
done
echo "    OK"

# ── gen/go directory (needed for worker Docker build) ──────────────────────────

echo "==> Checking gen/go exists"
if [[ ! -f "$REPO_ROOT/gen/go/go.mod" ]]; then
  echo "ERROR: gen/go/go.mod not found — run buf generate first" >&2
  exit 1
fi
echo "    OK"

# ── Database migration ─────────────────────────────────────────────────────────

echo "==> Starting postgres"
docker compose up -d postgres

echo "==> Waiting for postgres to be healthy..."
for i in $(seq 1 30); do
  docker compose ps postgres | grep -q "healthy" && break
  [[ $i -eq 30 ]] && { echo "ERROR: postgres did not become healthy in time" >&2; exit 1; }
  sleep 1
done
echo "    OK"

echo "==> Running migrations"
docker compose --profile migrate run --rm migrate
echo "    OK"

# ── Start full stack (Restate registers automatically) ────────────────────────

echo "==> Starting restate, worker, reviewer, api-server (restate-register runs automatically)"
docker compose up -d restate worker reviewer api-server restate-register

echo "==> Waiting for Restate admin API..."
for i in $(seq 1 30); do
  curl -sf "$RESTATE_ADMIN/health" &>/dev/null && break
  [[ $i -eq 30 ]] && { echo "ERROR: Restate admin API did not become ready in time" >&2; exit 1; }
  sleep 1
done
echo "    OK"

# ── Wait for restate-register to complete ─────────────────────────────────────

echo "==> Waiting for restate-register to complete..."
for i in $(seq 1 60); do
  STATUS=$(docker compose ps --all --format json restate-register 2>/dev/null | jq -r '.State // empty' 2>/dev/null || true)
  if [[ "$STATUS" == "exited" ]]; then
    EXIT_CODE=$(docker compose ps --all --format json restate-register 2>/dev/null | jq -r '.ExitCode // 1' 2>/dev/null || echo 1)
    if [[ "$EXIT_CODE" == "0" ]]; then
      echo "    OK (exited 0)"
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

# ── Verify registered services ─────────────────────────────────────────────────

echo "==> Verifying registered services"
SERVICES=$(curl -sf "$RESTATE_ADMIN/services" | jq -r '.services[].name')

FAILED=false
for svc in DiffFetcher PostReview PRReview Reviewer; do
  if echo "$SERVICES" | grep -qx "$svc"; then
    echo "    ✓ $svc"
  else
    echo "    ✗ $svc NOT FOUND" >&2
    FAILED=true
  fi
done

if $FAILED; then
  echo
  echo "ERROR: One or more services are missing from Restate." >&2
  echo "Registered services:"
  echo "$SERVICES" | sed 's/^/    /'
  exit 1
fi

echo
echo "All smoke tests passed."
