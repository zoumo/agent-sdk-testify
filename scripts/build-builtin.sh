#!/usr/bin/env bash
# Offline checked single-binary build; no SDK, Python, download or recording.
set -euo pipefail
builtin_source=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
if [[ $# != 1 ]]; then
  echo "usage: bash scripts/build-builtin.sh /absolute/output-binary" >&2
  exit 2
fi
mise exec -C "$builtin_source" -- go -C "$builtin_source" run -tags builtin_generate . --check
mise exec -C "$builtin_source" -- go -C "$builtin_source" build -o "$1" .
