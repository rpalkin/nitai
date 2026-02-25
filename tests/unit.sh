#!/usr/bin/env bash
# Unit tests for go-services: build, vet, and package tests.
# Run from any directory: ./tests/unit.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GO_SERVICES="$REPO_ROOT/go-services"

cd "$GO_SERVICES"

echo "==> go vet ./..."
go vet ./...

echo "==> go build ./cmd/worker"
go build ./cmd/worker

echo "==> Compile-time: restate.Context implements context.Context"
TMPFILE="$(mktemp /tmp/restate_compat_XXXXXX.go)"
cat > "$TMPFILE" << 'EOF'
package main

import (
	"context"
	restate "github.com/restatedev/sdk-go"
)

var _ context.Context = (restate.Context)(nil)
var _ context.Context = (restate.ObjectContext)(nil)

func main() {}
EOF
# Copy into module so replace directives apply, then clean up
cp "$TMPFILE" restate_compat_check.go
go build restate_compat_check.go
rm -f restate_compat_check.go "$TMPFILE"
echo "    OK — both context types satisfy context.Context"

echo "==> go test ./internal/... -v"
go test ./internal/... -v

echo
echo "All unit tests passed."
