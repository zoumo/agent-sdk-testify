GO ?= go
MODULE ?= github.com/zoumo/agent-sdk-testify
VERSION ?= $(shell git describe --tags --exact-match 2>/dev/null || echo devel)
GO_LDFLAGS ?= -X=$(MODULE)/internal/version.buildVersion=$(VERSION)
GO_TEST_FLAGS ?= -buildvcs=true -ldflags=$(GO_LDFLAGS)
GOLANGCI_LINT ?= golangci-lint
PYTHON ?= python3
BIN_DIR ?= bin
ARTIFACT_DIR ?= .artifacts
BINARY ?= $(BIN_DIR)/agent-sdk-testify

.PHONY: all fmt lint fmt-check test test-integration test-e2e test-build test-race vet build bundle check release clean

all: check

fmt:
	$(GOLANGCI_LINT) fmt

lint:
	$(GOLANGCI_LINT) run ./...

fmt-check:
	$(GOLANGCI_LINT) fmt --diff

test:
	$(GO) test $(GO_TEST_FLAGS) . ./internal/...

test-integration:
	$(GO) test $(GO_TEST_FLAGS) ./tests/intergration/...

test-e2e:
	GO="$(GO)" bash scripts/with-agent-sdk-testify.sh --race $(GO) test $(GO_TEST_FLAGS) -race ./test/e2e/...

test-build:
	$(GO) test $(GO_TEST_FLAGS) ./tests/build/...

test-race:
	$(GO) test $(GO_TEST_FLAGS) -race . ./internal/... ./tests/intergration/...

vet:
	$(GO) vet ./...

build:
	@mkdir -p "$(dir $(BINARY))"
	$(GO) run -tags builtin_generate -ldflags=$(GO_LDFLAGS) ./cmd/agent-sdk-testify --check
	$(GO) build -trimpath -ldflags=$(GO_LDFLAGS) -o "$(BINARY)" ./cmd/agent-sdk-testify

bundle:
	PYTHON="$(PYTHON)" bash scripts/build.sh

check: fmt-check lint
	PYTHON="$(PYTHON)" bash scripts/check-offline.sh

release:
	@test -n "$(OUTPUT)" || { echo "usage: make release OUTPUT=/absolute/new-dist" >&2; exit 2; }
	$(PYTHON) scripts/release.py --output "$(OUTPUT)"

clean:
	rm -rf "$(BIN_DIR)" "$(ARTIFACT_DIR)"
