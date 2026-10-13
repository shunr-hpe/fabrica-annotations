# Copyright © 2025-2026 OpenCHAMI a Series of LF Projects, LLC
#
# SPDX-License-Identifier: MIT


# Tool binary name
BINARY_NAME=fab-init
GO=go
GOFLAGS=-v

GO_FILES=$(shell find $(FAB_INIT_DIR) -name "*.go" -type f)
BINARY_DIR=bin
BUILD_DIR=build
FAB_INIT_DIR=fab-init

# Stepped-mode postgres settings (see test-postgres / postgres-up below)
PG_CONTAINER ?= user-pg
PG_PORT ?= 5432
PG_URL = postgres://postgres:postgres@localhost:$(PG_PORT)/user_service?sslmode=disable

# Default target
.DEFAULT_GOAL := help

# Run all build targets
.PHONY: all
all: clean build generate

.PHONY: help
help: ## Display this help screen
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

# Build the binary
.PHONY: build
build: $(BINARY_DIR)/$(BINARY_NAME) ## Build the fab-init binary

$(BINARY_DIR)/$(BINARY_NAME): $(GO_FILES)
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BINARY_DIR)
	cd $(FAB_INIT_DIR) && $(GO) build $(GOFLAGS) -o ../$(BINARY_DIR)/$(BINARY_NAME) .

# Generate the user-service variants under build/
.PHONY: generate
generate: generate-sqlite generate-postgres ## Generate all user-service variants under build/

.PHONY: generate-sqlite
generate-sqlite: ## Generate the sqlite user-service under build/user-service-sqlite
	@echo "Generating user-service-sqlite..."
	./scripts/generate-user-service --name user-service-sqlite --db sqlite

.PHONY: generate-postgres
generate-postgres: ## Generate the postgres user-service under build/user-service-postgres
	@echo "Generating user-service-postgres..."
	./scripts/generate-user-service --name user-service-postgres --db postgres

# Run integration tests against the generated sqlite user-service
.PHONY: test-sqlite
test-sqlite: ## Run the sqlite user-service integration tests
	@echo "Running sqlite user-service tests..."
	cd tests/sqlite && $(GO) test $(GOFLAGS) ./...

# Run integration tests against the generated postgres user-service.
# With no USER_SERVICE_POSTGRES_URL set, the test starts and stops its own
# postgres container (port 5432 if free, otherwise a free port).
.PHONY: test-postgres
test-postgres: ## Run the postgres user-service integration tests (manages its own postgres)
	@echo "Running postgres user-service tests..."
	cd tests/postgres && $(GO) test $(GOFLAGS) ./...

# --- stepped postgres workflow ----------------------------------------------
# For manual, step-through testing:
#   make postgres-up                # start postgres
#   make service-postgres-run       # run the service in the foreground (new shell)
#   make test-postgres-only-tests   # run tests against the running postgres
#   ... do your own curl testing against http://localhost:8080 ...
#   make postgres-down              # stop postgres

.PHONY: postgres-up
postgres-up: ## Start a postgres container for manual/stepped testing
	docker run --rm -d --name $(PG_CONTAINER) -p $(PG_PORT):5432 \
		-e POSTGRES_USER=postgres \
		-e POSTGRES_PASSWORD=postgres \
		-e POSTGRES_DB=user_service \
		postgres:16
	@echo "postgres is up on port $(PG_PORT)"
	@echo 'export USER_SERVICE_POSTGRES_URL="$(PG_URL)"'

.PHONY: postgres-down
postgres-down: ## Stop the manual/stepped postgres container (and its data volume)
	-docker rm -f -v $(PG_CONTAINER)

.PHONY: service-postgres-run
service-postgres-run: ## Run the generated postgres user-service in the foreground (uses PG_URL)
	cd $(BUILD_DIR)/user-service-postgres && \
		$(GO) run ./cmd/server serve --port 8080 --database-url "$(PG_URL)"

.PHONY: test-postgres-only-tests
test-postgres-only-tests: ## Run the postgres tests against an already-running postgres (see postgres-up)
	@echo "Running postgres user-service tests against $(PG_URL)..."
	cd tests/postgres && USER_SERVICE_POSTGRES_URL="$(PG_URL)" $(GO) test $(GOFLAGS) ./...

# Clean build artifacts
.PHONY: clean
clean: ## Clean local build artifacts
	@echo "Cleaning build artifacts..."
	rm -rf $(BINARY_DIR)
	rm -rf $(BUILD_DIR)
