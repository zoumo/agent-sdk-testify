#!/usr/bin/env bash
# Build the checked tool binary and official driver wheel; no SDK, network or recording.
set -euo pipefail
source=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
if [[ $# != 0 ]]; then
  echo "usage: bash scripts/build.sh" >&2
  exit 2
fi
output=${AGENT_SDK_TESTIFY_BUILD_DIR:-"$source/.artifacts/build"}
[[ "$output" = /* ]] || output="$source/$output"
if [[ -f "$source/.git" ]]; then
  export AGENT_SDK_TESTIFY_BUILD_DIR="$output"
  exec bash "$source/scripts/with-build-checkout.sh" bash scripts/build.sh
fi
mkdir -p "$output"
version=$(git -C "$source" describe --tags --exact-match 2>/dev/null || printf devel)
ldflags="-X=github.com/zoumo/agent-sdk-testify/internal/version.buildVersion=$version"
go -C "$source" run -tags builtin_generate ./cmd/agent-sdk-testify --check
go -C "$source" build -ldflags="$ldflags" -o "$output/agent-sdk-testify" ./cmd/agent-sdk-testify
AGENT_SDK_TESTIFY_VERSION="$version" \
  "${PYTHON:-python3}" -m pip wheel --no-deps --no-build-isolation --wheel-dir "$output" "$source/drivers"
