.PHONY: build run test lint clean

APP_NAME := claude-code-go
BUILD_DIR := ./dist
CMD := ./cmd/server

# Build the binary
build:
	@echo "Building $(APP_NAME)..."
	go build -o $(BUILD_DIR)/$(APP_NAME) $(CMD)

# Run the server
run:
	go run $(CMD) -config ./configs/config.yaml

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
