SHELL := /bin/bash

GO_INSTALL_DIR ?= $(HOME)/.local/trackflights-go
GO ?= $(if $(wildcard $(GO_INSTALL_DIR)/current/bin/go),$(GO_INSTALL_DIR)/current/bin/go,go)
NPM ?= npm
PYTHON ?= python3
APT_GET ?= apt-get
SUDO ?= sudo
CC ?= gcc
OPENCV_PKG_CONFIG_LIBDIR ?= /usr/lib/$(shell $(CC) -dumpmachine)/pkgconfig:/usr/share/pkgconfig
GOCACHE ?= $(CURDIR)/.cache/go-build
DATA_ROOT ?= $(CURDIR)/data
BIN_DIR ?= $(CURDIR)/bin
# Cross-compilation target; native run/build targets use the host architecture.
PI_ARCH ?= arm64
PI_ARM ?= 7
PI_OUTPUT_DIR ?= $(BIN_DIR)/linux-$(if $(filter arm,$(PI_ARCH)),armv$(PI_ARM),$(PI_ARCH))
SDR_LISTEN ?= 127.0.0.1:8090
SDR_URL ?= http://127.0.0.1:8090
CALLBACK_URL ?= http://127.0.0.1:8080
SDR_DEVICE ?= 0
SDR_GAIN ?= auto
SDR_PPM ?= 0
DOCKER ?= docker
NOSTR_CONTAINER ?= nostr-relay
NOSTR_IMAGE ?= ghcr.io/mattn/nostr-relay:latest
NOSTR_NATIVE_IMAGE ?= trackflights-nostr-relay:pi
NOSTR_SOURCE ?= https://github.com/mattn/nostr-relay.git\#v0.0.250
NOSTR_BIND ?= 127.0.0.1
NOSTR_PORT ?= 7447
NOSTR_DATA_DIR ?= $(DATA_ROOT)/nostr-relay
NOSTR_RELAY_HOST ?= 127.0.0.1
NOSTR_RELAY ?= ws://$(NOSTR_RELAY_HOST):$(NOSTR_PORT)
NOSTR_KEY_FILE ?= $(DATA_ROOT)/sdr/nostr.key
NOSTR_HTTP_URL ?= http://127.0.0.1:$(NOSTR_PORT)
# The analysis relay has separate defaults from the existing flight relay.
NOSTR_ANALYSIS_CONTAINER ?= nostr-relay-analysis
NOSTR_ANALYSIS_IMAGE ?= $(NOSTR_IMAGE)
NOSTR_ANALYSIS_BIND ?= $(NOSTR_BIND)
NOSTR_ANALYSIS_PORT ?= 7448
NOSTR_ANALYSIS_DATA_DIR ?= $(DATA_ROOT)/nostr-relay-analysis
NOSTR_ANALYSIS_HTTP_URL ?= http://127.0.0.1:$(NOSTR_ANALYSIS_PORT)

.DEFAULT_GOAL := help
.PHONY: help install-tracking build-tracking test-tracking run-tracking install-python install-go install-sdr initial-setup frontend-install frontend-build typecheck test test-local test-go-installer test-nostr-relays test-pi test-sdr run run-local run-sdr stop-local build build-sdr build-pi build-sdr-pi run-pi run-sdr-pi run-local-pi clean nostr-pull-flight-data nostr-start-flight-data nostr-stop-flight-data nostr-restart-flight-data nostr-status-flight-data nostr-logs-flight-data nostr-check-flight-data nostr-build-native-flight-data nostr-pull-analysis nostr-start-analysis nostr-stop-analysis nostr-restart-analysis nostr-status-analysis nostr-logs-analysis nostr-check-analysis validate-analysis-relay

help: ## Show available commands.
	@awk 'BEGIN {FS = ":.*##"}; /^[a-zA-Z_-]+:.*##/ {printf "\033[36m%-18s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

install-python: ## Install Ubuntu Python 3 and certificates using apt.
	$(SUDO) $(APT_GET) update
	$(SUDO) $(APT_GET) install -y python3 ca-certificates

install-sdr: ## Install Ubuntu RTL-SDR tools, library and USB rules using apt.
	$(SUDO) $(APT_GET) update
	$(SUDO) $(APT_GET) install -y rtl-sdr

install-tracking: ## Install Ubuntu OpenCV development libraries and FFmpeg for object tracking.
	$(SUDO) $(APT_GET) update
	$(SUDO) $(APT_GET) install -y libopencv-dev libopencv-contrib-dev pkg-config g++ ffmpeg

build-tracking: ## Build the optional OpenCV object tracking worker.
	@mkdir -p "$(BIN_DIR)"
	PKG_CONFIG_LIBDIR="$(OPENCV_PKG_CONFIG_LIBDIR)" CGO_ENABLED=1 GOCACHE="$(GOCACHE)" $(GO) build -tags opencv -o "$(BIN_DIR)/tracking-worker" ./cmd/tracking-worker

test-tracking: build-tracking ## Run the migrated OpenCV tracking and worker integration tests.
	PKG_CONFIG_LIBDIR="$(OPENCV_PKG_CONFIG_LIBDIR)" CGO_ENABLED=1 GOCACHE="$(GOCACHE)" $(GO) test -tags opencv ./internal/objecttracking/engine
	TRACKFLIGHTS_INTEGRATION_WORKER="$(BIN_DIR)/tracking-worker" GOCACHE="$(GOCACHE)" $(GO) test ./internal/api -run TestTrackingOpenCVIntegration -count=1

run-tracking: frontend-build build-tracking ## Build the tracking worker and run the web app.
	TRACKFLIGHTS_TRACKING_WORKER="$(BIN_DIR)/tracking-worker" TRACKFLIGHTS_DATA_ROOT="$(DATA_ROOT)" GOCACHE="$(GOCACHE)" $(GO) run ./cmd/trackflights

initial-setup: ## Install Ubuntu Python, latest Go and RTL-SDR dependencies.
	$(MAKE) install-python
	$(MAKE) install-go
	$(MAKE) install-sdr

install-go: ## Download and install the latest stable Go for this Linux machine.
	$(PYTHON) "$(CURDIR)/scripts/install-go.py" "$(GO_INSTALL_DIR)"

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

test-pi: ## Run Go and local-service tests without rebuilding the frontend.
	GOCACHE="$(GOCACHE)" $(GO) test ./...
	$(MAKE) test-local

test-sdr: ## Test the SDR service without Node.js or SDR hardware.
	GOCACHE="$(GOCACHE)" $(GO) test ./internal/sdr/...

test-go-installer: ## Test Go installer architecture and integrity handling.
	PYTHONDONTWRITEBYTECODE=1 $(PYTHON) -m unittest discover -s scripts -p 'install_go_test.py'

test-nostr-relays: ## Test named relay configuration without Docker.
	PYTHONDONTWRITEBYTECODE=1 $(PYTHON) -m unittest discover -s scripts -p 'nostr_relays_test.py'

test-local: test-go-installer test-nostr-relays ## Test local service startup and shutdown without SDR hardware.
	bash "$(CURDIR)/scripts/local-services-test.sh"

run: frontend-build ## Build the UI and start the local server at http://127.0.0.1:8080.
	TRACKFLIGHTS_DATA_ROOT=$(DATA_ROOT) GOCACHE=$(GOCACHE) $(GO) run ./cmd/trackflights

run-sdr: build-sdr ## Start only the SDR receiver (Ctrl-C to stop).
	"$(BIN_DIR)/sdr" -listen "$(SDR_LISTEN)" -device "$(SDR_DEVICE)" -gain "$(SDR_GAIN)" -ppm "$(SDR_PPM)" -nostr-relay "$(NOSTR_RELAY)" -nostr-key-file "$(NOSTR_KEY_FILE)"

run-local: build ## Start the SDR receiver and main app together on this computer.
	@DATA_ROOT="$(DATA_ROOT)" BIN_DIR="$(BIN_DIR)" SDR_LISTEN="$(SDR_LISTEN)" SDR_URL="$(SDR_URL)" CALLBACK_URL="$(CALLBACK_URL)" SDR_DEVICE="$(SDR_DEVICE)" SDR_GAIN="$(SDR_GAIN)" SDR_PPM="$(SDR_PPM)" NOSTR_RELAY="$(NOSTR_RELAY)" NOSTR_KEY_FILE="$(NOSTR_KEY_FILE)" bash "$(CURDIR)/scripts/local-services.sh" start

run-pi: ## Run the prebuilt Raspberry Pi main app; use PI_ARCH=arm for 32-bit.
	TRACKFLIGHTS_DATA_ROOT="$(DATA_ROOT)" "$(PI_OUTPUT_DIR)/trackflights"

run-sdr-pi: ## Run the prebuilt Raspberry Pi SDR app without building it.
	"$(PI_OUTPUT_DIR)/sdr" -listen "$(SDR_LISTEN)" -device "$(SDR_DEVICE)" -gain "$(SDR_GAIN)" -ppm "$(SDR_PPM)" -nostr-relay "$(NOSTR_RELAY)" -nostr-key-file "$(NOSTR_KEY_FILE)"

run-local-pi: ## Run both prebuilt Raspberry Pi apps together.
	@DATA_ROOT="$(DATA_ROOT)" BIN_DIR="$(PI_OUTPUT_DIR)" SDR_LISTEN="$(SDR_LISTEN)" SDR_URL="$(SDR_URL)" CALLBACK_URL="$(CALLBACK_URL)" SDR_DEVICE="$(SDR_DEVICE)" SDR_GAIN="$(SDR_GAIN)" SDR_PPM="$(SDR_PPM)" NOSTR_RELAY="$(NOSTR_RELAY)" NOSTR_KEY_FILE="$(NOSTR_KEY_FILE)" bash "$(CURDIR)/scripts/local-services.sh" start

stop-local: ## Stop both applications started by run-local or run-local-pi.
	@DATA_ROOT="$(DATA_ROOT)" bash "$(CURDIR)/scripts/local-services.sh" stop

build: frontend-build build-sdr ## Build both production binaries in bin/.
	@mkdir -p $(BIN_DIR)
	GOCACHE=$(GOCACHE) $(GO) build -o $(BIN_DIR)/trackflights ./cmd/trackflights

build-sdr: ## Build the SDR receiver without the frontend.
	@mkdir -p "$(BIN_DIR)"
	GOCACHE=$(GOCACHE) $(GO) build -o "$(BIN_DIR)/sdr" ./cmd/sdr

# Build on any supported host for a Raspberry Pi without replacing host binaries.
build-pi: frontend-build build-sdr-pi ## Cross-compile both binaries for Linux ARM64; PI_ARCH=arm for 32-bit.
	@mkdir -p "$(PI_OUTPUT_DIR)"
	GOOS=linux GOARCH="$(PI_ARCH)" GOARM="$(PI_ARM)" CGO_ENABLED=0 GOCACHE="$(GOCACHE)" $(GO) build -o "$(PI_OUTPUT_DIR)/trackflights" ./cmd/trackflights

build-sdr-pi: ## Cross-compile only the SDR binary for Linux ARM64; PI_ARCH=arm for 32-bit.
	@if [[ "$(PI_ARCH)" != arm64 && "$(PI_ARCH)" != arm ]]; then echo "PI_ARCH must be arm64 or arm" >&2; exit 1; fi
	@if [[ "$(PI_ARCH)" == arm && "$(PI_ARM)" != 5 && "$(PI_ARM)" != 6 && "$(PI_ARM)" != 7 ]]; then echo "PI_ARM must be 5, 6 or 7" >&2; exit 1; fi
	@mkdir -p "$(PI_OUTPUT_DIR)"
	GOOS=linux GOARCH="$(PI_ARCH)" GOARM="$(PI_ARM)" CGO_ENABLED=0 GOCACHE="$(GOCACHE)" $(GO) build -o "$(PI_OUTPUT_DIR)/sdr" ./cmd/sdr

NOSTR_ENV = DOCKER="$(DOCKER)" NOSTR_CONTAINER="$(NOSTR_CONTAINER)" NOSTR_IMAGE="$(NOSTR_IMAGE)" NOSTR_BIND="$(NOSTR_BIND)" NOSTR_PORT="$(NOSTR_PORT)" NOSTR_DATA_DIR="$(NOSTR_DATA_DIR)" NOSTR_HTTP_URL="$(NOSTR_HTTP_URL)"

nostr-build-native-flight-data: ## Build the relay image for this machine (including Raspberry Pi).
	$(DOCKER) build -t "$(NOSTR_NATIVE_IMAGE)" "$(NOSTR_SOURCE)"

nostr-pull-flight-data: ## Download the Nostr relay image.
	@$(NOSTR_ENV) bash "$(CURDIR)/scripts/nostr-relay.sh" pull

nostr-start-flight-data: ## Start the existing relay or create one with persistent data.
	@$(NOSTR_ENV) bash "$(CURDIR)/scripts/nostr-relay.sh" start

nostr-stop-flight-data: ## Stop the Nostr relay without deleting its data.
	@$(NOSTR_ENV) bash "$(CURDIR)/scripts/nostr-relay.sh" stop

nostr-restart-flight-data: ## Restart the Nostr relay.
	@$(NOSTR_ENV) bash "$(CURDIR)/scripts/nostr-relay.sh" restart

nostr-status-flight-data: ## Show relay state, ports and data mounts.
	@$(NOSTR_ENV) bash "$(CURDIR)/scripts/nostr-relay.sh" status

nostr-logs-flight-data: ## Show the last 100 relay log lines.
	@$(NOSTR_ENV) bash "$(CURDIR)/scripts/nostr-relay.sh" logs

nostr-check-flight-data: ## Check relay availability and fetch its NIP-11 information.
	@$(NOSTR_ENV) bash "$(CURDIR)/scripts/nostr-relay.sh" check

# Analysis relay: independent container, host port and persistent database.
NOSTR_ANALYSIS_ENV = DOCKER="$(DOCKER)" NOSTR_CONTAINER="$(NOSTR_ANALYSIS_CONTAINER)" NOSTR_IMAGE="$(NOSTR_ANALYSIS_IMAGE)" NOSTR_BIND="$(NOSTR_ANALYSIS_BIND)" NOSTR_PORT="$(NOSTR_ANALYSIS_PORT)" NOSTR_DATA_DIR="$(NOSTR_ANALYSIS_DATA_DIR)" NOSTR_HTTP_URL="$(NOSTR_ANALYSIS_HTTP_URL)"

nostr-pull-analysis: ## Download the analysis relay image.
	@$(NOSTR_ANALYSIS_ENV) bash "$(CURDIR)/scripts/nostr-relay.sh" pull

validate-analysis-relay:
	@if [[ "$(NOSTR_ANALYSIS_CONTAINER)" == "$(NOSTR_CONTAINER)" ]]; then echo "Analysis and flight relays must use different container names" >&2; exit 1; fi
	@if [[ "$$(realpath -m -- "$(NOSTR_ANALYSIS_DATA_DIR)")" == "$$(realpath -m -- "$(NOSTR_DATA_DIR)")" ]]; then echo "Analysis and flight relays must use different data directories" >&2; exit 1; fi

nostr-start-analysis: validate-analysis-relay ## Start the analysis relay with its own database and port.
	@$(NOSTR_ANALYSIS_ENV) bash "$(CURDIR)/scripts/nostr-relay.sh" start

nostr-stop-analysis: ## Stop the analysis relay without deleting data.
	@$(NOSTR_ANALYSIS_ENV) bash "$(CURDIR)/scripts/nostr-relay.sh" stop

nostr-restart-analysis: ## Restart the analysis relay.
	@$(NOSTR_ANALYSIS_ENV) bash "$(CURDIR)/scripts/nostr-relay.sh" restart

nostr-status-analysis: ## Show analysis relay status, ports and data mount.
	@$(NOSTR_ANALYSIS_ENV) bash "$(CURDIR)/scripts/nostr-relay.sh" status

nostr-logs-analysis: ## Show the last 100 analysis relay log lines.
	@$(NOSTR_ANALYSIS_ENV) bash "$(CURDIR)/scripts/nostr-relay.sh" logs

nostr-check-analysis: ## Check the analysis relay and fetch its NIP-11 information.
	@$(NOSTR_ANALYSIS_ENV) bash "$(CURDIR)/scripts/nostr-relay.sh" check

clean: ## Remove generated build artifacts and local Go cache.
	rm -rf $(BIN_DIR) $(CURDIR)/.cache frontend/node_modules frontend/dist
