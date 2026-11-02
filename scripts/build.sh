#!/usr/bin/env bash
# Build the checked tool binary and official driver wheel; no SDK, network or recording.
set -euo pipefail
source=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
if [[ $# != 0 ]]; then
  echo "usage: bash scripts/build.sh" >&2
  exit 2
fi
output=${AGENT_SDK_TESTIFY_BUILD_DIR:-"$source/.artifacts/build"}
mkdir -p "$output"
go -C "$source" run -tags builtin_generate . --check
go -C "$source" build -o "$output/agent-sdk-testify" .
"${PYTHON:-python3}" -m pip wheel --no-deps --no-build-isolation --wheel-dir "$output" "$source/drivers"
