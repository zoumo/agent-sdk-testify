#!/usr/bin/env bash
# Local convenience entry; consumers invoke the installed binary directly.
set -euo pipefail
simulation_source=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
simulation_build=${AGENT_SDK_TESTIFY_BUILD_DIR:-"$simulation_source/.artifacts/simulation"}
case "${1:---help}" in
 build)
   [[ $# == 1 ]] || exit 2
   mkdir -p "$simulation_build"
   bash "$simulation_source/scripts/build-builtin.sh" "$simulation_build/agent-sdk-testify"
   "${PYTHON:-python3.11}" -m pip wheel --no-deps --no-build-isolation --wheel-dir "$simulation_build" "$simulation_source/drivers"
   ;;
 record|verify|mock) exec "$simulation_build/agent-sdk-testify" "$@" ;;
 --help|-h) echo 'Usage: scripts/simulation.sh build|record|verify|mock [flags]' ;;
 *) echo 'expected build, record, verify or mock' >&2; exit 2 ;;
esac
