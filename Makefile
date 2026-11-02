GO ?= go
GOLANGCI_LINT ?= golangci-lint
PYTHON ?= python3
ARTIFACT_DIR ?= .artifacts
BINARY ?= $(ARTIFACT_DIR)/agent-sdk-testify

.PHONY: all fmt lint fmt-check test test-race vet build bundle check release clean

all: check

fmt:
	$(GOLANGCI_LINT) fmt

lint:
	$(GOLANGCI_LINT) run ./...

fmt-check:
	$(GOLANGCI_LINT) fmt --diff

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

build:
	@mkdir -p "$(ARTIFACT_DIR)"
	$(GO) run -tags builtin_generate . --check
	$(GO) build -trimpath -o "$(BINARY)" .

bundle:
	PYTHON="$(PYTHON)" bash scripts/build.sh

check: fmt-check lint
	PYTHON="$(PYTHON)" bash scripts/check-offline.sh

release:
	@test -n "$(OUTPUT)" || { echo "usage: make release OUTPUT=/absolute/new-dist" >&2; exit 2; }
	$(PYTHON) scripts/release.py --output "$(OUTPUT)"

clean:
	rm -rf "$(ARTIFACT_DIR)"
