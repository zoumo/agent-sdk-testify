#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
python=${PYTHON:-python3}
"$python" -c "import setuptools; assert tuple(map(int, setuptools.__version__.split('.')[:2])) >= (68, 0)"
export GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOWORK=off PIP_NO_INDEX=1
export GORACE="${GORACE:-} atexit_sleep_ms=0"
version=$(git describe --tags --exact-match 2>/dev/null || printf devel)
ldflags="-X=github.com/zoumo/agent-sdk-testify/internal/version.buildVersion=$version"
export AGENT_SDK_TESTIFY_VERSION="$version"
mkdir -p .artifacts/acceptance
: > .artifacts/acceptance/phase-times.tsv
phase() {
  local name=$1 started=$SECONDS
  shift
  "$@"
  printf '%s\t%s\n' "$name" "$((SECONDS-started))" >> .artifacts/acceptance/phase-times.tsv
}
go mod verify
go run -tags builtin_generate -ldflags="$ldflags" ./cmd/agent-sdk-testify --check
phase unit go test -buildvcs=true -ldflags="$ldflags" -json -count=1 -timeout=5m . ./internal/... > .artifacts/acceptance/ut.jsonl
phase integration go test -buildvcs=true -ldflags="$ldflags" -json -count=1 -timeout=5m ./tests/intergration/... > .artifacts/acceptance/integration.jsonl
phase unit-race go test -buildvcs=true -ldflags="$ldflags" -json -race -count=1 -timeout=10m . ./internal/... > .artifacts/acceptance/race.jsonl
phase integration-race go test -buildvcs=true -ldflags="$ldflags" -json -race -count=1 -timeout=10m ./tests/intergration/... > .artifacts/acceptance/integration-race.jsonl
phase build-normal go build -ldflags="$ldflags" -o .artifacts/acceptance/agent-sdk-testify ./cmd/agent-sdk-testify
phase build-race go build -ldflags="$ldflags" -race -o .artifacts/acceptance/agent-sdk-testify-race ./cmd/agent-sdk-testify
export AGENT_SDK_TESTIFY_NORMAL_BIN="$PWD/.artifacts/acceptance/agent-sdk-testify"
export AGENT_SDK_TESTIFY_RACE_BIN="$PWD/.artifacts/acceptance/agent-sdk-testify-race"
go version -m "$AGENT_SDK_TESTIFY_NORMAL_BIN" > .artifacts/acceptance/cli-build-info.txt
go version -m "$AGENT_SDK_TESTIFY_RACE_BIN" >> .artifacts/acceptance/cli-build-info.txt
phase e2e-race env AGENT_SDK_TESTIFY_BIN="$AGENT_SDK_TESTIFY_RACE_BIN" bash scripts/with-agent-sdk-testify.sh --race go test -buildvcs=true -json -race -count=1 -timeout=10m ./test/e2e/... > .artifacts/acceptance/e2e-race.jsonl
phase build-checks go test -buildvcs=true -json -count=1 -timeout=5m ./tests/build/... > .artifacts/acceptance/build.jsonl
go vet ./...
go list -deps -test ./... > .artifacts/acceptance/dependencies.txt
"$python" -c "from pathlib import Path; assert 'github.com/zoumo/agent-sdk-go' not in Path('.artifacts/acceptance/dependencies.txt').read_text()"
export AGENT_SDK_TESTIFY_BIN="$AGENT_SDK_TESTIFY_NORMAL_BIN"
export PYTHONPATH="$PWD/drivers/common:$PWD/drivers/codex/official:$PWD/drivers/claude/official"
phase scripts "$python" -m unittest discover -s scripts -p 'test_*.py'
phase drivers "$python" -m unittest discover -s drivers/tests -p 'test_*.py'
phase scenarios "$python" -m unittest discover -s drivers/tests/scenarios -p 'test_*.py'
rm -rf .artifacts/acceptance/dist
phase release "$python" scripts/release.py --allow-untagged --output .artifacts/acceptance/dist
phase wheel "$python" scripts/check-agent-sdk-testify-package.py .artifacts/acceptance/dist/*.whl
