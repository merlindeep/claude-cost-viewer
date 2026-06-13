# ccview — Makefile
#
# Common developer tasks. Run `make help` for a summary.

BINARY      := ccview
PKG         := github.com/merlindeep/claude-cost-viewer
CMD         := ./cmd/ccview
BIN_DIR     := bin

# Version metadata, injected into the binary via -ldflags.
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE        ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w \
	-X $(PKG)/internal/buildinfo.Version=$(VERSION) \
	-X $(PKG)/internal/buildinfo.Commit=$(COMMIT) \
	-X $(PKG)/internal/buildinfo.Date=$(DATE)

GO          ?= go

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the binary into ./bin
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY) $(CMD)

.PHONY: install
install: ## Install the binary into $GOBIN (or $GOPATH/bin)
	$(GO) install -trimpath -ldflags '$(LDFLAGS)' $(CMD)

.PHONY: run
run: ## Build and run with ARGS (e.g. make run ARGS="--once")
	$(GO) run -ldflags '$(LDFLAGS)' $(CMD) $(ARGS)

.PHONY: test
test: ## Run the test suite
	$(GO) test ./...

.PHONY: test-race
test-race: ## Run the test suite with the race detector
	$(GO) test -race ./...

.PHONY: cover
cover: ## Run tests and print coverage per package
	$(GO) test -covermode=atomic -coverprofile=coverage.txt ./...
	$(GO) tool cover -func=coverage.txt | tail -1

.PHONY: cover-html
cover-html: cover ## Generate an HTML coverage report
	$(GO) tool cover -html=coverage.txt -o coverage.html
	@echo "wrote coverage.html"

.PHONY: fmt
fmt: ## Format all Go source
	$(GO) fmt ./...

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: lint
lint: ## Run golangci-lint (must be installed)
	golangci-lint run

.PHONY: tidy
tidy: ## Tidy go.mod / go.sum
	$(GO) mod tidy

.PHONY: check
check: fmt vet test ## Format, vet, and test

.PHONY: snapshot
snapshot: ## Build a local release snapshot with GoReleaser (no publish)
	goreleaser release --snapshot --clean

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) dist coverage.txt coverage.html
