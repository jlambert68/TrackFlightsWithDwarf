SHELL := /bin/bash

GO ?= go
NPM ?= npm
GOCACHE ?= $(CURDIR)/.cache/go-build
DATA_ROOT ?= $(CURDIR)/data
BIN_DIR ?= $(CURDIR)/bin
SDR_LISTEN ?= 127.0.0.1:8090
SDR_URL ?= http://127.0.0.1:8090
CALLBACK_URL ?= http://127.0.0.1:8080
SDR_DEVICE ?= 0
SDR_GAIN ?= auto
SDR_PPM ?= 0

.DEFAULT_GOAL := help
.PHONY: help frontend-install frontend-build typecheck test test-local run run-local run-sdr stop-local build build-sdr clean

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
	$(MAKE) test-local

test-local: ## Test local service startup and shutdown without SDR hardware.
	bash "$(CURDIR)/scripts/local-services-test.sh"

run: frontend-build ## Build the UI and start the local server at http://127.0.0.1:8080.
	TRACKFLIGHTS_DATA_ROOT=$(DATA_ROOT) GOCACHE=$(GOCACHE) $(GO) run ./cmd/trackflights

run-sdr: build-sdr ## Start only the SDR receiver (Ctrl-C to stop).
	"$(BIN_DIR)/sdr" -listen "$(SDR_LISTEN)" -device "$(SDR_DEVICE)" -gain "$(SDR_GAIN)" -ppm "$(SDR_PPM)"

run-local: build ## Start the SDR receiver and main app together on this computer.
	@DATA_ROOT="$(DATA_ROOT)" BIN_DIR="$(BIN_DIR)" SDR_LISTEN="$(SDR_LISTEN)" SDR_URL="$(SDR_URL)" CALLBACK_URL="$(CALLBACK_URL)" SDR_DEVICE="$(SDR_DEVICE)" SDR_GAIN="$(SDR_GAIN)" SDR_PPM="$(SDR_PPM)" bash "$(CURDIR)/scripts/local-services.sh" start

stop-local: ## Stop both applications started by run-local.
	@DATA_ROOT="$(DATA_ROOT)" bash "$(CURDIR)/scripts/local-services.sh" stop

build: frontend-build build-sdr ## Build both production binaries in bin/.
	@mkdir -p $(BIN_DIR)
	GOCACHE=$(GOCACHE) $(GO) build -o $(BIN_DIR)/trackflights ./cmd/trackflights

build-sdr: ## Build the SDR receiver without the frontend.
	@mkdir -p "$(BIN_DIR)"
	GOCACHE=$(GOCACHE) $(GO) build -o "$(BIN_DIR)/sdr" ./cmd/sdr

clean: ## Remove generated build artifacts and local Go cache.
	rm -rf $(BIN_DIR) $(CURDIR)/.cache frontend/node_modules frontend/dist
