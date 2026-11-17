GO ?= go
PYTHON ?= python3
BINARY := dist/tenkacloud-simulator
START_LISTEN ?= 127.0.0.1:4566
START_STATE ?= ./practice.db
START_TOKEN_FILE ?= ./simulator-token
GO_TEST_PACKAGES ?= ./...
COVERAGE_PROFILE ?= reports/coverage.out
export GO START_LISTEN START_STATE START_TOKEN_FILE

.PHONY: build dev start test-start test test-scripts test-go vet format-check before-commit e2e formal verify-assets cross-check
build:
	mkdir -p dist
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags='-s -w' -o $(BINARY) ./cmd/simulator

dev: build
	./$(BINARY)

start:
	@sh scripts/start.sh

test-start:
	$(PYTHON) test/start/check_start.py

test: test-scripts test-go
	$(PYTHON) scripts/check-coverage.py $(COVERAGE_PROFILE)

test-scripts:
	$(PYTHON) -m unittest discover -s scripts/verification -p 'test_*.py'
	$(PYTHON) -m unittest discover -s test/battle/image -p 'test_*.py'
	$(PYTHON) scripts/smithy-contracts.py --check
	$(PYTHON) scripts/test-smithy-contracts.py
	$(PYTHON) scripts/test-software-factory.py
	$(PYTHON) scripts/test-factory-acceptance.py
	$(PYTHON) scripts/factory_acceptance.py
	$(PYTHON) scripts/test-coverage-gate.py

test-go:
	mkdir -p reports
	# Packages each compile fixed WASM runtimes. Avoid cross-package CPU contention;
	# goroutine/race and deadline assertions inside each test remain unchanged.
	# CI names a file holding a -run pattern to run one share of a package's tests.
	$(GO) test -p 1 -race -coverpkg=./... -coverprofile=$(COVERAGE_PROFILE) $(if $(GO_TEST_RUN_FILE),-v -run "$$(cat '$(GO_TEST_RUN_FILE)')") $(GO_TEST_PACKAGES)

vet:
	$(GO) vet ./...

format-check:
	@test -z "$$($(GO) fmt ./...)" || { echo 'Go formatting changed; review and rerun'; exit 1; }

verify-assets:
	$(PYTHON) scripts/check-assets.py

before-commit: format-check vet verify-assets
	$(GO) mod verify
	mkdir -p reports
	$(MAKE) test build

e2e: build
	$(PYTHON) test/e2e/reflow_test.py
	$(PYTHON) test/e2e/check.py
	$(MAKE) test-start

formal:
	$(PYTHON) scripts/check-formal.py

cross-check:
	$(PYTHON) scripts/check-cross-build.py

.PHONY: audit
audit: build
	$(PYTHON) test/e2e/audit.py

# Optional evidence wrappers; native gates and merge policy are unchanged.
.PHONY: verify verify_release
verify:
	$(PYTHON) scripts/verification/run.py fast

verify_release:
	$(PYTHON) scripts/verification/run.py release
