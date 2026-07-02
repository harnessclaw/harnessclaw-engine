.PHONY: build run kill-ports prepare-runtime runtime-bundle test lint clean

APP_NAME := harnessclaw-engine
BUILD_DIR := ./dist
CMD := ./cmd/server
NODE ?= node
#CONFIG ?= ./configs/config.yaml
CONFIG ?= ./configs/config_self.yaml
OUTPUT_DIR ?= .runtime/bin
ARCHIVE_DIR ?= dist
PLATFORM ?=
ARCH ?=
WS_PORT ?= 8081
CONSOLE_PORT ?= 8090
export GOTOOLCHAIN ?= auto

# Build the binary
build:
	@echo "Building $(APP_NAME)..."
	go build -o $(BUILD_DIR)/$(APP_NAME) $(CMD)

# Run the server
run: kill-ports prepare-runtime
	CLAUDE_TOOLS_BROWSER_AGENT_BINARY_PATH="$$(HARNESSCLAW_RUNTIME_PLATFORM="$(PLATFORM)" HARNESSCLAW_RUNTIME_ARCH="$(ARCH)" $(NODE) build/prepare-runtime.cjs --output-dir "$(OUTPUT_DIR)" --print-agent-browser-path)" go run $(CMD) -config $(CONFIG)

# Clear stale local listeners before binding dev ports.
kill-ports:
	@for port in $(WS_PORT) $(CONSOLE_PORT); do \
		pids="$$(lsof -tiTCP:$$port -sTCP:LISTEN 2>/dev/null || true)"; \
		if [ -n "$$pids" ]; then \
			echo "Killing listeners on port $$port: $$pids"; \
			kill $$pids 2>/dev/null || true; \
			sleep 0.2; \
		fi; \
	done

# Prepare native runtime sidecars for local standalone engine runs.
prepare-runtime:
	HARNESSCLAW_RUNTIME_PLATFORM="$(PLATFORM)" HARNESSCLAW_RUNTIME_ARCH="$(ARCH)" $(NODE) build/prepare-runtime.cjs --output-dir "$(OUTPUT_DIR)"

# Build a publishable engine runtime bundle:
# dist/harnessclaw-engine-runtime-<platform>-<arch>.zip
runtime-bundle:
	HARNESSCLAW_RUNTIME_PLATFORM="$(PLATFORM)" HARNESSCLAW_RUNTIME_ARCH="$(ARCH)" $(NODE) build/prepare-runtime.cjs --include-engine --archive-dir "$(ARCHIVE_DIR)"

# Run tests
test:
	go test ./... -v -race -count=1

# Run tests with coverage
test-cover:
	go test ./... -v -race -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html

# Lint
lint:
	golangci-lint run ./...

# Format
fmt:
	gofmt -s -w .
	goimports -w .

# Tidy modules
tidy:
	go mod tidy

# Clean build artifacts
clean:
	rm -rf $(BUILD_DIR) coverage.out coverage.html

# Vulnerability check
vuln:
	govulncheck ./...
