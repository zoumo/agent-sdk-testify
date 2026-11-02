#!/usr/bin/env bash
# Repository convenience entry. Runtime orchestration stays in agent-sdk-testify.
set -euo pipefail
simulation_source=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
simulation_build=${AGENT_SDK_TESTIFY_BUILD_DIR:-"$simulation_source/.artifacts/simulation"}
simulation_python=${PYTHON:-python3.11}
usage() {
  cat <<'USAGE'
Usage: bash scripts/simulation.sh build
       bash scripts/simulation.sh record|replay codex|claude [runner flags]
       bash scripts/simulation.sh simulate [runner flags]
       bash scripts/simulation.sh review|qualify|adopt|gate [corpus flags]
Build creates agent-sdk-testify, sdk-driver, and the official-driver wheel.
Set AGENT_SDK_TESTIFY_BUILD_DIR to choose the build directory; PYTHON selects
the Python 3.11+ build interpreter (setuptools>=68 must already be installed).
Install the wheel's [codex], [claude], or [recording] extras in your own venv,
then put its bin directory on PATH before official record/replay.
record automatically runs official replay and replaces qualified corpus entries.
Use the Go binary directly for private capture-only work. Go simulation is separate.
record also accepts --index and --archive; other runner flags pass through unchanged.
USAGE
}
case "${1:---help}" in
  review|qualify|adopt|gate)
    simulation_mode=$1
    shift
    if [[ "$simulation_mode" == gate ]]; then
      mise exec -C "$simulation_source" -- go -C "$simulation_source" build -o "$simulation_build/agent-sdk-testify" ./tools/agent-sdk-testify
      mise exec -C "$simulation_source" -- go -C "$simulation_source" build -o "$simulation_build/sdk-driver" ./tests/simulation/drivers
      set -- --sdk-driver "$simulation_build/sdk-driver" --source-root "$simulation_source" "$@"
    fi
    exec "$simulation_build/agent-sdk-testify" corpus "$simulation_mode" \
      --index "$simulation_source/tests/simulation/recordings/index.json" \
      --scenarios "$simulation_source/tests/simulation/scenarios" "$@"
    ;;
  build)
    [[ $# == 1 ]] || { usage >&2; exit 2; }
    mkdir -p "$simulation_build"
    simulation_build=$(cd "$simulation_build" && pwd)
    test "$(mise exec -C "$simulation_source" -- go env GOVERSION)" = go1.26.9
    mise exec -C "$simulation_source" -- go -C "$simulation_source" build -o "$simulation_build/agent-sdk-testify" ./tools/agent-sdk-testify
    mise exec -C "$simulation_source" -- go -C "$simulation_source" build -o "$simulation_build/sdk-driver" ./tests/simulation/drivers
    "$simulation_python" -m pip wheel --no-deps --no-build-isolation --wheel-dir "$simulation_build" "$simulation_source/tests/simulation/drivers"
    ;;
  record|replay)
    simulation_mode=$1
    case "${2:-}" in codex|claude) simulation_agent=$2 ;; *) usage >&2; exit 2 ;; esac
    shift 2
    simulation_driver=$(command -v "agent-sdk-testify-$simulation_agent") || {
      echo "Install the official-driver wheel and selected SDK extra in your venv." >&2
      exit 2
    }
    if [[ "$simulation_mode" == record && " $* " != *" --help "* && " $* " != *" -h "* ]]; then
      exec "$simulation_build/agent-sdk-testify" record \
        --index "$simulation_source/tests/simulation/recordings/index.json" \
        --scenarios "$simulation_source/tests/simulation/scenarios" \
        --provider "$simulation_agent" --driver "$simulation_driver" "$@"
    fi
    exec "$simulation_build/agent-sdk-testify" "$simulation_mode" --provider "$simulation_agent" --driver "$simulation_driver" "$@"
    ;;
  simulate)
    shift
    exec "$simulation_build/agent-sdk-testify" simulate --driver "$simulation_build/sdk-driver" "$@"
    ;;
  --help|-h) usage ;;
  *) usage >&2; exit 2 ;;
esac
