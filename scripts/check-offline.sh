#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
python=${PYTHON:-python3}
task=${1:-all}
if (( $# > 1 )); then
  echo "usage: $0 [all|quality|test|race|e2e|build]" >&2
  exit 2
fi
export GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOWORK=off PIP_NO_INDEX=1
export GORACE="${GORACE:-} atexit_sleep_ms=0"
version=$(git describe --tags --exact-match 2>/dev/null || printf devel)
ldflags="-X=github.com/zoumo/agent-sdk-testify/internal/version.buildVersion=$version"
export AGENT_SDK_TESTIFY_VERSION="$version"
artifact_dir=.artifacts/acceptance
mkdir -p "$artifact_dir"
: > "$artifact_dir/phase-times.tsv"
phase() {
  local name=$1 started=$SECONDS
  shift
  "$@"
  printf '%s\t%s\n' "$name" "$((SECONDS-started))" >> "$artifact_dir/phase-times.tsv"
}

check_environment() {
  "$python" -c "import setuptools; assert tuple(map(int, setuptools.__version__.split('.')[:2])) >= (68, 0)"
}

quality_checks() {
  go mod verify
  go run -tags builtin_generate -ldflags="$ldflags" ./cmd/agent-sdk-testify --check
  go vet ./...
  go list -deps -test ./... > "$artifact_dir/dependencies.txt"
  "$python" -c "from pathlib import Path; assert 'github.com/zoumo/agent-sdk-go' not in Path('$artifact_dir/dependencies.txt').read_text()"
}

normal_tests() {
  phase unit go test -buildvcs=true -ldflags="$ldflags" -json -count=1 -timeout=5m . ./internal/... > "$artifact_dir/ut.jsonl"
  phase integration go test -buildvcs=true -ldflags="$ldflags" -json -count=1 -timeout=5m ./tests/intergration/... > "$artifact_dir/integration.jsonl"
}

race_tests() {
  phase unit-race go test -buildvcs=true -ldflags="$ldflags" -json -race -count=1 -timeout=10m . ./internal/... > "$artifact_dir/race.jsonl"
  phase integration-race go test -buildvcs=true -ldflags="$ldflags" -json -race -count=1 -timeout=10m ./tests/intergration/... > "$artifact_dir/integration-race.jsonl"
}

build_normal() {
  phase build-normal go build -ldflags="$ldflags" -o "$artifact_dir/agent-sdk-testify" ./cmd/agent-sdk-testify
}

build_race() {
  phase build-race go build -ldflags="$ldflags" -race -o "$artifact_dir/agent-sdk-testify-race" ./cmd/agent-sdk-testify
}

build_binaries() {
  build_normal
  build_race
  export AGENT_SDK_TESTIFY_NORMAL_BIN="$PWD/$artifact_dir/agent-sdk-testify"
  export AGENT_SDK_TESTIFY_RACE_BIN="$PWD/$artifact_dir/agent-sdk-testify-race"
  go version -m "$AGENT_SDK_TESTIFY_NORMAL_BIN" > "$artifact_dir/cli-build-info.txt"
  go version -m "$AGENT_SDK_TESTIFY_RACE_BIN" >> "$artifact_dir/cli-build-info.txt"
}

e2e_tests() {
  phase e2e-race env AGENT_SDK_TESTIFY_BIN="$AGENT_SDK_TESTIFY_RACE_BIN" bash scripts/with-agent-sdk-testify.sh --race go test -buildvcs=true -json -race -count=1 -timeout=10m ./test/e2e/... > "$artifact_dir/e2e-race.jsonl"
}

build_and_package_checks() {
  [[ -n "${AGENT_SDK_TESTIFY_NORMAL_BIN:-}" ]] || {
    echo "build_and_package_checks requires AGENT_SDK_TESTIFY_NORMAL_BIN" >&2
    exit 2
  }
  phase build-checks go test -buildvcs=true -json -count=1 -timeout=5m ./tests/build/... > "$artifact_dir/build.jsonl"
  export AGENT_SDK_TESTIFY_BIN="$AGENT_SDK_TESTIFY_NORMAL_BIN"
  export PYTHONPATH="$PWD/drivers/common:$PWD/drivers/codex/official:$PWD/drivers/claude/official"
  phase scripts "$python" -m unittest discover -s scripts -p 'test_*.py'
  phase drivers "$python" -m unittest discover -s drivers/tests -p 'test_*.py'
  phase scenarios "$python" -m unittest discover -s drivers/tests/scenarios -p 'test_*.py'
  rm -rf "$artifact_dir/dist"
  phase release "$python" scripts/release.py --allow-untagged --output "$artifact_dir/dist"
  phase wheel "$python" scripts/check-agent-sdk-testify-package.py "$artifact_dir"/dist/*.whl
}

case "$task" in
  all)
    check_environment
    quality_checks
    normal_tests
    race_tests
    build_binaries
    e2e_tests
    build_and_package_checks
    ;;
  quality)
    check_environment
    quality_checks
    ;;
  test)
    check_environment
    normal_tests
    ;;
  race)
    check_environment
    race_tests
    ;;
  e2e)
    check_environment
    build_race
    export AGENT_SDK_TESTIFY_RACE_BIN="$PWD/$artifact_dir/agent-sdk-testify-race"
    e2e_tests
    ;;
  build)
    check_environment
    build_binaries
    build_and_package_checks
    ;;
  *)
    echo "usage: $0 [all|quality|test|race|e2e|build]" >&2
    exit 2
    ;;
esac
