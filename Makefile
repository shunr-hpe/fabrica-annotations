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

# Default target
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

# Generate the user-service under build/
.PHONY: generate
generate: ## Generate the user-service under build/
	@echo "Generating user-service..."
	./scripts/generate-user-service

# Clean build artifacts
.PHONY: clean
clean: ## Clean local build artifacts
	@echo "Cleaning build artifacts..."
	rm -rf $(BINARY_DIR)
	rm -rf $(BUILD_DIR)
