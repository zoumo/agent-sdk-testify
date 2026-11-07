#!/usr/bin/env bash
# Build once for the selected mode, then share the actual CLI with all test packages.
set -euo pipefail
race=false
if [[ "${1:-}" == --race ]]; then race=true; shift; fi
if [[ $# == 0 ]]; then
  echo "usage: $0 [--race] COMMAND [ARG ...] (optionally set AGENT_SDK_TESTIFY_BIN)" >&2
  exit 2
fi
testify_go=${GO:-go}
if [[ -n "${AGENT_SDK_TESTIFY_BIN:-}" ]]; then
  [[ "$AGENT_SDK_TESTIFY_BIN" = /* && -f "$AGENT_SDK_TESTIFY_BIN" && -x "$AGENT_SDK_TESTIFY_BIN" ]] || {
    echo "AGENT_SDK_TESTIFY_BIN must be an absolute executable file" >&2
    exit 2
  }
else
  testify_source=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
  testify_build_dir=$(mktemp -d "${TMPDIR:-/tmp}/agent-sdk-testify.XXXXXXXX")
  trap 'rm -rf "$testify_build_dir"' EXIT
  export AGENT_SDK_TESTIFY_BIN="$testify_build_dir/agent-sdk-testify"
  build_flags=(build)
  if "$race"; then build_flags+=(-race); fi
  GOPROXY=off GOSUMDB=off "$testify_go" -C "$testify_source" "${build_flags[@]}" -o "$AGENT_SDK_TESTIFY_BIN" ./cmd/agent-sdk-testify
fi
identity=$("$testify_go" version -m "$AGENT_SDK_TESTIFY_BIN")
package=$(awk '$1 == "path" { print $2 }' <<< "$identity")
actual_race=$(awk '$1 == "build" && $2 == "-race=true" { print "true" }' <<< "$identity")
[[ "$package" == github.com/zoumo/agent-sdk-testify/cmd/agent-sdk-testify ]] || { echo "unexpected CLI package: $package" >&2; exit 2; }
[[ "${actual_race:-false}" == "$race" ]] || { echo "CLI race mode mismatch: artifact race=${actual_race:-false}, requested race=$race" >&2; exit 2; }
export AGENT_SDK_TESTIFY_BIN
export GORACE="${GORACE:-} atexit_sleep_ms=0"
printf 'CLI: %s (race=%s)\n' "$AGENT_SDK_TESTIFY_BIN" "$race" >&2
"$@"
