GO ?= go
TEMPL_VERSION := v0.3.1020
STATICCHECK_VERSION := v0.7.0
GOVULNCHECK_VERSION := v1.6.0
ACTIONLINT_VERSION := v1.7.12
GORELEASER_VERSION := v2.17.1

.PHONY: help bootstrap generate generated-check fmt fmt-check lint workflow-lint release-check test test-race build run doctor vuln compose-up compose-down compose-config check clean

help:
	@echo "Litebox development targets"
	@echo "  bootstrap      download modules and install pinned tools"
	@echo "  generate       regenerate committed templ output"
	@echo "  generated-check verify committed generated output"
	@echo "  fmt            format Go and templ sources"
	@echo "  fmt-check      verify Go formatting without changing files"
	@echo "  lint           run go vet and staticcheck"
	@echo "  workflow-lint  validate GitHub Actions workflow syntax"
	@echo "  release-check  validate the GoReleaser configuration"
	@echo "  test           run the complete test suite"
	@echo "  test-race      run tests with the race detector"
	@echo "  build          build bin/litebox"
	@echo "  run            start the local server"
	@echo "  doctor         verify local data integrity"
	@echo "  vuln           scan dependencies for known vulnerabilities"
	@echo "  compose-up     start the one-container stack"
	@echo "  check          run release-grade local verification"

bootstrap:
	$(GO) mod download
	$(GO) install github.com/a-h/templ/cmd/templ@$(TEMPL_VERSION)
	$(GO) install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)
	$(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

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

workflow-lint:
	$(GO) run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

release-check:
	$(GO) run github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION) check

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

compose-up:
	docker compose up -d --build

compose-down:
	docker compose down

compose-config:
	docker compose config --quiet

check: generated-check fmt-check lint workflow-lint release-check test-race vuln build compose-config

clean:
	$(GO) clean
	@rm -rf bin coverage dist
