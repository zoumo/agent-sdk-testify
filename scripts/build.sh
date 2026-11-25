#!/usr/bin/env bash
# Build the checked tool binary and the official driver packages from local dependencies.
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
go -C "$source" build -trimpath -ldflags="$ldflags" -o "$output/agent-sdk-testify" ./cmd/agent-sdk-testify
work=$(mktemp -d "${TMPDIR:-/tmp}/testify-codex.XXXXXXXX")
trap 'rm -rf "$work"' EXIT
bash "$source/drivers/codex/rust/install.sh" "$work/build" "$work/prefix"
install -m 755 "$work/prefix/bin/agent-sdk-testify-codex" "$output/agent-sdk-testify-codex"
bash "$source/scripts/build-claude-driver.sh" "$output"
