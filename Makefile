GO ?= go
PYTHON ?= python3
BINARY := dist/tenkacloud-simulator
START_LISTEN ?= 127.0.0.1:4566
START_STATE ?= ./practice.db
START_TOKEN_FILE ?= ./simulator-token
export GO START_LISTEN START_STATE START_TOKEN_FILE

.PHONY: build dev start test-start test vet format-check before-commit e2e formal verify-assets cross-check
build:
	mkdir -p dist
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags='-s -w' -o $(BINARY) ./cmd/simulator

dev: build
	./$(BINARY)

start:
	@sh scripts/start.sh

test-start:
	$(PYTHON) test/start/check_start.py

test:
	$(PYTHON) scripts/test-software-factory.py
	mkdir -p reports
	# Packages each compile fixed WASM runtimes. Avoid cross-package CPU contention;
	# goroutine/race and deadline assertions inside each test remain unchanged.
	$(GO) test -p 1 -race -coverpkg=./... -coverprofile=reports/coverage.out ./...
	$(PYTHON) scripts/test-coverage-gate.py
	$(PYTHON) scripts/check-coverage.py reports/coverage.out

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
