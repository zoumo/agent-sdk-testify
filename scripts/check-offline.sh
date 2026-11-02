#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
[[ "$(go env GOVERSION)" == go1.26.9 ]] || { echo 'Go 1.26.9 required' >&2; exit 2; }
export GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOWORK=off PIP_NO_INDEX=1
export GORACE=atexit_sleep_ms=0
mkdir -p .artifacts/acceptance
go mod verify
go run -tags builtin_generate . --check
go test -json -count=1 -timeout=5m ./... > .artifacts/acceptance/ut.jsonl
go test -json -race -count=1 -timeout=10m ./... > .artifacts/acceptance/race.jsonl
go vet ./...
go list -deps -test ./... > .artifacts/acceptance/dependencies.txt
python3.11 -c "from pathlib import Path; assert 'github.com/zoumo/agent-sdk-go' not in Path('.artifacts/acceptance/dependencies.txt').read_text()"
go build -o .artifacts/acceptance/agent-sdk-testify .
export AGENT_SDK_TESTIFY_BIN="$PWD/.artifacts/acceptance/agent-sdk-testify"
export PYTHONPATH="$PWD/drivers/common:$PWD/drivers/codex/official:$PWD/drivers/claude/official"
python3.11 -m unittest discover -s scripts -p 'test_*.py'
python3.11 -m unittest discover -s drivers/tests -p 'test_*.py'
python3.11 -m unittest discover -s drivers/tests/scenarios -p 'test_*.py'
python3.11 scripts/release.py --output .artifacts/acceptance/dist
python3.11 scripts/check-agent-sdk-testify-package.py .artifacts/acceptance/dist/*.whl
