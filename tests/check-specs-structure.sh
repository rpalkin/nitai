#!/usr/bin/env bash
set -euo pipefail

SPECS_DIR="$(cd "$(dirname "$0")/../specs" && pwd)"
CLAUDE_MD="$(cd "$(dirname "$0")/.." && pwd)/CLAUDE.md"
errors=0

check() {
  if ! "$@" 2>/dev/null; then
    echo "FAIL: $*"
    ((errors++))
  fi
}

# Domain folders exist
for domain in review-pipeline indexing providers data-model deployment testing; do
  check test -d "$SPECS_DIR/$domain"
  check test -f "$SPECS_DIR/$domain/$domain.md"
done

# Plans folder exists with phase files
check test -d "$SPECS_DIR/plans"
check test -f "$SPECS_DIR/plans/phases.md"
check test -f "$SPECS_DIR/plans/phase1-plan.md"
check test -f "$SPECS_DIR/plans/phase2-plan.md"

# Backlog exists (renamed from later.md)
check test -f "$SPECS_DIR/backlog.md"

# Monolithic files removed
check test ! -f "$SPECS_DIR/overview.md"
check test ! -f "$SPECS_DIR/scratch.md"
check test ! -f "$SPECS_DIR/later.md"
check test ! -f "$SPECS_DIR/e2e-cases.md"

# CLAUDE.md has spec update instruction
check grep -q "update the relevant spec" "$CLAUDE_MD"

if [ "$errors" -gt 0 ]; then
  echo "$errors check(s) failed"
  exit 1
fi
echo "All specs structure checks passed"