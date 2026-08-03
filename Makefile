# Each of these directories is a separate Go module (own go.mod), so tooling is
# run once per module. The root .golangci.yml is shared by all of them.
MODULES := . lfu policies policies/arc policies/tinylfu metrics bandit bandit/redis benchclient bench examples/basic examples/migration

GOLANGCI_LINT_VERSION := v2.8.0

.PHONY: all
all: fmt vet lint test release-check ## Format, vet, lint, test and check releasability

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
		( cd $$m && go test -race -short -count=1 ./... ); \
	done

.PHONY: release-check
release-check: ## Check the repository could actually be released today
	@./scripts/release-check.sh

.PHONY: evidence
evidence: ## Replay the workload suite and print the policy comparison tables
	( cd bench && go test -count=1 -timeout 20m -v ./... )

# The bandit/redis tests run against miniredis by default, which is a fake.
# The Lua the adapter depends on - TIME inside a script, SET NX PX, HINCRBY on
# a key the script names itself - is exactly what a fake can be too permissive
# about, so the same suite runs against real servers here. See
# docker-compose.yml for why both engines are covered rather than one.
COMPOSE ?= docker compose

.PHONY: redis-up
redis-up: ## Start the local Valkey and Redis containers and wait for them
	$(COMPOSE) up -d --wait

.PHONY: redis-down
redis-down: ## Stop the local Valkey and Redis containers
	$(COMPOSE) down -v

.PHONY: redis-test
redis-test: ## Run the bandit/redis suite against real Valkey and Redis
	@$(MAKE) redis-up
	@status=0; \
		for target in "valkey 127.0.0.1:63799" "redis 127.0.0.1:63798"; do \
			set -- $$target; \
			echo "==> bandit/redis against $$1 ($$2)"; \
			( cd bandit/redis && AS_CACHE_REDIS_ADDR=$$2 go test -race -count=1 ./... ) || status=1; \
		done; \
		$(MAKE) redis-down; \
		exit $$status

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
