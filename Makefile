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
	$(GO) test -race -coverpkg=./... -coverprofile=reports/coverage.out ./...
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
	$(PYTHON) test/e2e/check.py

formal:
	$(PYTHON) scripts/check-formal.py

cross-check:
	$(PYTHON) scripts/check-cross-build.py

.PHONY: audit
audit: build
	$(PYTHON) test/e2e/audit.py
