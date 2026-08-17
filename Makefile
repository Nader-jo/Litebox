GO ?= go
TEMPL_VERSION := v0.3.1020
STATICCHECK_VERSION := v0.7.0
GOVULNCHECK_VERSION := v1.6.0
ACTIONLINT_VERSION := v1.7.12
GOLANGCI_LINT_VERSION := v2.12.2

.PHONY: help setup bootstrap generate generated-check fmt fmt-check lint golangci-lint workflow-lint test test-race build run doctor vuln container-smoke compose-up compose-down compose-pull compose-config dev-image check clean

help:
	@echo "Litebox development targets"
	@echo "  setup          prepare and validate a contributor checkout"
	@echo "  bootstrap      download modules and install pinned tools"
	@echo "  generate       regenerate committed templ output"
	@echo "  generated-check verify committed generated output"
	@echo "  fmt            format Go and templ sources"
	@echo "  fmt-check      verify Go formatting without changing files"
	@echo "  lint           run go vet and staticcheck"
	@echo "  golangci-lint  run the pinned community linter suite"
	@echo "  workflow-lint  validate GitHub Actions workflow syntax"
	@echo "  test           run the complete test suite"
	@echo "  test-race      run tests with the race detector"
	@echo "  build          build bin/litebox"
	@echo "  run            start the local server"
	@echo "  doctor         verify local data integrity"
	@echo "  vuln           scan dependencies for known vulnerabilities"
	@echo "  container-smoke build and health-check the local container"
	@echo "  compose-up     build source and start the local Compose stack"
	@echo "  compose-pull   pull the published multi-platform image"
	@echo "  dev-image      build the reproducible development image"
	@echo "  check          run release-grade local verification"

setup: bootstrap generate compose-config
	$(GO) test ./internal/config ./internal/ui
	@echo "Litebox contributor environment is ready. Run 'make run' to start it."

bootstrap:
	$(GO) mod download
	$(GO) install github.com/a-h/templ/cmd/templ@$(TEMPL_VERSION)
	$(GO) install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)
	$(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	$(GO) install github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

generate:
	$(GO) run github.com/a-h/templ/cmd/templ@$(TEMPL_VERSION) generate

generated-check: generate
	git diff --exit-code -- internal/ui

fmt: generate
	$(GO) fmt ./...

fmt-check:
	test -z "$$(gofmt -l .)"

lint: generate
	$(GO) vet ./...
	$(GO) run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...

golangci-lint: generate
	$(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run

workflow-lint:
	$(GO) run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

test: generate
	$(GO) test -cover ./...

test-race: generate
	$(GO) test -race ./...

build: generate
	$(GO) build -trimpath -o bin/litebox ./cmd/mailbox

run: generate
	$(GO) run ./cmd/mailbox serve

doctor:
	$(GO) run ./cmd/mailbox doctor --deep

vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

container-smoke:
	docker build -t litebox:smoke .
	bash scripts/smoke-container.sh litebox:smoke

compose-up:
	docker compose -f compose.yaml -f compose.build.yaml up -d --build

compose-down:
	docker compose -f compose.yaml -f compose.build.yaml down

compose-pull:
	docker compose pull mailbox

compose-config:
	docker compose --env-file .env.example config --quiet
	docker compose --env-file .env.example -f compose.yaml -f compose.build.yaml config --quiet

dev-image:
	docker build -f Dockerfile.dev -t litebox:dev .

check: generated-check fmt-check lint golangci-lint workflow-lint test-race vuln build compose-config

clean:
	$(GO) clean
	@rm -rf bin coverage dist
