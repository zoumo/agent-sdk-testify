#!/usr/bin/env bash
# Rebuild from the lockfile and package using only the prepared npm cache.
set -euo pipefail
source=$(cd "$(dirname "${BASH_SOURCE[0]}")/../drivers/claude/typescript" && pwd)
output=$1
cd "$source"
export npm_config_update_notifier=false
npm ci --offline --ignore-scripts --omit=optional --no-audit --no-fund
npm pack --offline --loglevel=error --pack-destination "$output"
