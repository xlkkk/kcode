# kcode — AI Coding Agent powered by Eino ADK
# ================================================

BINARY   := kcode
MODULE   := $(shell head -1 go.mod | awk '{print $$2}')
CMD      := ./cmd/kcode
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_AT := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')

# Go toolchain
GO       := go
GOFLAGS  := -trimpath
LDFLAGS  := -s -w -X main.version=$(VERSION) -X main.buildTime=$(BUILD_AT)

# Directories
BIN_DIR  := ./bin
DIST_DIR := ./dist
STATIC   := ./web/static

# Web 模式工作目录:workflow 流程的所有读写(代码生成/状态/沙箱)都落在此目录
GAME_DIR := /Users/xlk/go/src/game-dev-workflow

.PHONY: all build run clean test lint fmt vet tidy deps help
.PHONY: run-tui run-print run-remote run-web

# ──────────────────────────────────────────────
# Default
# ──────────────────────────────────────────────
all: tidy fmt vet build ## 完整构建流程

# ──────────────────────────────────────────────
# Build
# ──────────────────────────────────────────────
build: ## 构建二进制到 bin/
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) $(CMD)
	@mkdir -p $(BIN_DIR)/static
	@cp -r $(STATIC)/. $(BIN_DIR)/static/
	@echo "构建完成: $(BIN_DIR)/$(BINARY) (静态页面已复制到 $(BIN_DIR)/static)"

build-linux: ## 交叉编译 Linux amd64
	GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/$(BINARY)-linux-amd64 $(CMD)

build-darwin: ## 交叉编译 macOS arm64
	GOOS=darwin GOARCH=arm64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/$(BINARY)-darwin-arm64 $(CMD)

# ──────────────────────────────────────────────
# Run
# ──────────────────────────────────────────────
run: build ## 以 TUI 模式运行
	$(BIN_DIR)/$(BINARY)

run-print: build ## 以 print 模式运行（需 PROMPT=xxx）
	@test -n "$(PROMPT)" || { echo "Usage: make run-print PROMPT='your question'"; exit 1; }
	$(BIN_DIR)/$(BINARY) -p "$(PROMPT)"

run-remote: build ## 以 remote 模式运行（默认 :18888）
	$(BIN_DIR)/$(BINARY) --remote $(ADDR)

run-web: build ## 以 Web 模式运行（工作目录 $(GAME_DIR)，可 GAME_DIR=xxx 覆盖）
	$(BIN_DIR)/$(BINARY) --web --dir $(GAME_DIR)

# ──────────────────────────────────────────────
# Test & Lint
# ──────────────────────────────────────────────
test: ## 运行所有测试
	$(GO) test ./... -v -count=1

test-race: ## 运行测试（带竞态检测）
	$(GO) test ./... -v -race -count=1

test-cover: ## 运行测试并生成覆盖率报告
	$(GO) test ./... -coverprofile=coverage.out
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

lint: ## 运行 golangci-lint（需预装）
	golangci-lint run ./...

fmt: ## 格式化代码
	$(GO) fmt ./...

vet: ## 静态分析
	$(GO) vet ./...

# ──────────────────────────────────────────────
# Dependencies
# ──────────────────────────────────────────────
deps: ## 拉取 Eino 及核心依赖
	$(GO) get github.com/cloudwego/eino@latest
	$(GO) get github.com/cloudwego/eino-ext/components/model/openai@latest
	$(GO) get github.com/cloudwego/eino-ext/components/tool/filesystem@latest
	$(GO) get github.com/charmbracelet/bubbletea@latest
	$(GO) get github.com/charmbracelet/lipgloss@latest
	$(GO) get github.com/charmbracelet/glamour@latest

tidy: ## 整理依赖
	$(GO) mod tidy

# ──────────────────────────────────────────────
# Clean
# ──────────────────────────────────────────────
clean: ## 清理构建产物
	rm -rf $(BIN_DIR) $(DIST_DIR) coverage.out coverage.html

# ──────────────────────────────────────────────
# Help
# ──────────────────────────────────────────────
help: ## 显示帮助
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'
