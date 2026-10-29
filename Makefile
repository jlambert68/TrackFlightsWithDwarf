SHELL := /bin/bash

GO ?= go
NPM ?= npm
GOCACHE ?= $(CURDIR)/.cache/go-build
DATA_ROOT ?= $(CURDIR)/data
BIN_DIR ?= $(CURDIR)/bin

.DEFAULT_GOAL := help
.PHONY: help frontend-install frontend-build typecheck test run build clean

help: ## Show available commands.
	@awk 'BEGIN {FS = ":.*##"}; /^[a-zA-Z_-]+:.*##/ {printf "\033[36m%-18s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

frontend-install: ## Install locked frontend dependencies.
	$(NPM) --prefix frontend ci

frontend-build: ## Build the React frontend into the Go embedded-assets directory.
	@test -d frontend/node_modules || $(MAKE) frontend-install
	$(NPM) --prefix frontend run build

typecheck: ## Typecheck the React frontend.
	@test -d frontend/node_modules || $(MAKE) frontend-install
	$(NPM) --prefix frontend run typecheck

test: frontend-build ## Run Go and frontend validation.
	GOCACHE=$(GOCACHE) $(GO) test ./...

run: frontend-build ## Build the UI and start the local server at http://127.0.0.1:8080.
	TRACKFLIGHTS_DATA_ROOT=$(DATA_ROOT) GOCACHE=$(GOCACHE) $(GO) run ./cmd/trackflights

build: frontend-build ## Build a production binary in bin/trackflights.
	@mkdir -p $(BIN_DIR)
	GOCACHE=$(GOCACHE) $(GO) build -o $(BIN_DIR)/trackflights ./cmd/trackflights

clean: ## Remove generated build artifacts and local Go cache.
	rm -rf $(BIN_DIR) $(CURDIR)/.cache frontend/node_modules frontend/dist
