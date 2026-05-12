BINARY    := itential-job-metrics-exporter
MODULE    := github.com/itential/job-metrics-exporter
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS   := -s -w -X main.version=$(VERSION)
BUILD_DIR := dist

# Detect host OS and arch for the local 'build' target
UNAME_OS   := $(shell uname -s | tr '[:upper:]' '[:lower:]')
UNAME_ARCH := $(shell uname -m | sed 's/x86_64/amd64/;s/arm64/arm64/')

.PHONY: all build clean install deps lint test release release-all hooks

all: build

deps:
	go mod tidy
	go mod download

# Build a binary for the host machine (works on Linux and macOS, intel or Apple Silicon)
build: deps
	@mkdir -p $(BUILD_DIR)
	GOOS=$(UNAME_OS) GOARCH=$(UNAME_ARCH) go build -ldflags "$(LDFLAGS)" \
		-o $(BUILD_DIR)/$(BINARY)-$(UNAME_OS)-$(UNAME_ARCH) ./cmd/exporter
	@echo "Built $(BUILD_DIR)/$(BINARY)-$(UNAME_OS)-$(UNAME_ARCH)"

# Linux targets (production)
release-linux-amd64: deps
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" \
		-o $(BUILD_DIR)/$(BINARY)-linux-amd64 ./cmd/exporter
	@echo "Built $(BUILD_DIR)/$(BINARY)-linux-amd64"

release-linux-arm64: deps
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" \
		-o $(BUILD_DIR)/$(BINARY)-linux-arm64 ./cmd/exporter
	@echo "Built $(BUILD_DIR)/$(BINARY)-linux-arm64"

# macOS targets (local testing)
release-darwin-amd64: deps
	@mkdir -p $(BUILD_DIR)
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" \
		-o $(BUILD_DIR)/$(BINARY)-darwin-amd64 ./cmd/exporter
	@echo "Built $(BUILD_DIR)/$(BINARY)-darwin-amd64"

release-darwin-arm64: deps
	@mkdir -p $(BUILD_DIR)
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" \
		-o $(BUILD_DIR)/$(BINARY)-darwin-arm64 ./cmd/exporter
	@echo "Built $(BUILD_DIR)/$(BINARY)-darwin-arm64"

# Build all release targets (linux only — darwin targets are available locally via release-darwin-*)
release-all: release-linux-amd64 release-linux-arm64
	@echo "All targets built in $(BUILD_DIR)/"

test:
	go test -v -race ./...

lint:
	golangci-lint run ./...

# Install to system (run as root)
install: release-linux-amd64
	install -Dm755 $(BUILD_DIR)/$(BINARY)-linux-amd64 /usr/local/bin/$(BINARY)
	install -Dm640 config.example.yaml /etc/itential-job-metrics-exporter/config.yaml
	install -Dm644 itential-job-metrics-exporter.service /etc/systemd/system/$(BINARY).service
	@echo "Run: systemctl daemon-reload && systemctl enable --now $(BINARY)"

clean:
	rm -rf $(BUILD_DIR)

# hooks — install git hooks from .githooks/ into .git/hooks
hooks:
	@for hook in .githooks/*; do \
		name=$$(basename $$hook); \
		cp $$hook .git/hooks/$$name; \
		chmod +x .git/hooks/$$name; \
		echo "Installed .git/hooks/$$name"; \
	done
