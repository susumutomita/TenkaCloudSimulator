GO ?= go
PYTHON ?= python3
BINARY := dist/tenkacloud-simulator

.PHONY: build dev test vet format-check before-commit e2e formal verify-assets cross-check
build:
	mkdir -p dist
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags='-s -w' -o $(BINARY) ./cmd/simulator

dev: build
	./$(BINARY)

test:
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

formal:
	$(PYTHON) scripts/check-formal.py

cross-check:
	$(PYTHON) scripts/check-cross-build.py

.PHONY: audit
audit: build
	$(PYTHON) test/e2e/audit.py
