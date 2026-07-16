# Each of these directories is a separate Go module (own go.mod), so tooling is
# run once per module. The root .golangci.yml is shared by all of them.
MODULES := . lfu examples/basic examples/migration

GOLANGCI_LINT_VERSION := v2.8.0

.PHONY: all
all: fmt vet lint test ## Format, vet, lint and test every module

.PHONY: lint
lint: ## Run golangci-lint across all modules
	@set -e; for m in $(MODULES); do \
		echo "==> lint $$m"; \
		( cd $$m && golangci-lint run ./... ); \
	done

.PHONY: lint-fix
lint-fix: ## Run golangci-lint with --fix and apply formatters across all modules
	@set -e; for m in $(MODULES); do \
		echo "==> lint-fix $$m"; \
		( cd $$m && golangci-lint run --fix ./... && golangci-lint fmt ./... ); \
	done

.PHONY: fmt
fmt: ## Apply gofmt/goimports via the golangci-lint formatters
	@set -e; for m in $(MODULES); do \
		echo "==> fmt $$m"; \
		( cd $$m && golangci-lint fmt ./... ); \
	done

.PHONY: vet
vet: ## Run go vet across all modules
	@set -e; for m in $(MODULES); do \
		echo "==> vet $$m"; \
		( cd $$m && go vet ./... ); \
	done

.PHONY: test
test: ## Run tests with the race detector across all modules
	@set -e; for m in $(MODULES); do \
		echo "==> test $$m"; \
		( cd $$m && go test -race -count=1 ./... ); \
	done

.PHONY: tidy
tidy: ## Run go mod tidy across all modules
	@set -e; for m in $(MODULES); do \
		echo "==> tidy $$m"; \
		( cd $$m && go mod tidy ); \
	done

.PHONY: install-tools
install-tools: ## Install golangci-lint at the pinned version
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'
