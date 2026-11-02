#!/usr/bin/env bash
# Build this checkout’s unified test tool once, then run a caller-selected SDK check.
# Usage: bash scripts/with-agent-sdk-testify.sh mise exec -- go test ./...
# Or supply an already built absolute AGENT_SDK_TESTIFY_BIN; no source checkout needed.
set -euo pipefail
if [[ $# == 0 ]]; then
  echo "usage: $0 COMMAND [ARG ...] (optionally set AGENT_SDK_TESTIFY_BIN)" >&2
  exit 2
fi
if [[ -n "${AGENT_SDK_TESTIFY_BIN:-}" ]]; then
  [[ "$AGENT_SDK_TESTIFY_BIN" = /* && -f "$AGENT_SDK_TESTIFY_BIN" && -x "$AGENT_SDK_TESTIFY_BIN" ]] || {
    echo "AGENT_SDK_TESTIFY_BIN must be an absolute executable file" >&2
    exit 2
  }
else
  testify_source=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
  test "$(mise exec -C "$testify_source" -- go env GOVERSION)" = go1.26.9
  testify_build_dir=$(mktemp -d "${TMPDIR:-/tmp}/agent-sdk-testify.XXXXXXXX")
  trap 'rm -rf "$testify_build_dir"' EXIT
  export AGENT_SDK_TESTIFY_BIN="$testify_build_dir/agent-sdk-testify"
  GOPROXY=off GOSUMDB=off mise exec -C "$testify_source" -- go -C "$testify_source" build -o "$AGENT_SDK_TESTIFY_BIN" ./tools/agent-sdk-testify
fi
export AGENT_SDK_TESTIFY_BIN
"$AGENT_SDK_TESTIFY_BIN" version >&2
"$@"
