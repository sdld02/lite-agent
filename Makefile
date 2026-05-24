# lite-agent 构建工具
# 用法:
#   make build       - 构建当前平台
#   make build-all   - 构建所有平台 (Linux / macOS / Windows)
#   make clean       - 清理构建产物
#   make help        - 显示帮助

# 项目信息
APP_NAME    := lite-agent
MODULE      := lite-agent
BUILD_DIR   := bin
MAIN_FILE   := main.go

# 版本信息（可通过命令行传入）
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u '+%Y-%m-%d_%H:%M:%S')
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")

# Go 编译器标志
LDFLAGS := -s -w \
	-X 'main.Version=$(VERSION)' \
	-X 'main.BuildTime=$(BUILD_TIME)' \
	-X 'main.GitCommit=$(GIT_COMMIT)'

# 目标平台
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

# 颜色输出
GREEN  := \033[0;32m
CYAN   := \033[0;36m
RESET  := \033[0m

.PHONY: all build build-all clean help

# 默认目标：构建当前平台
all: build

## 构建当前平台
build:
	@echo "$(CYAN)🔨 构建当前平台...$(RESET)"
	@mkdir -p $(BUILD_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME) $(MAIN_FILE)
	@echo "$(GREEN)✅ 构建完成: $(BUILD_DIR)/$(APP_NAME)$(RESET)"

## 构建所有平台
build-all:
	@echo "$(CYAN)🔨 开始跨平台构建...$(RESET)"
	@mkdir -p $(BUILD_DIR)
	@for platform in $(PLATFORMS); do \
		GOOS=$${platform%/*} ; \
		GOARCH=$${platform#*/} ; \
		output=$(BUILD_DIR)/$(APP_NAME)-$${GOOS}-$${GOARCH} ; \
		if [ "$${GOOS}" = "windows" ]; then \
			output=$${output}.exe ; \
		fi ; \
		echo "  → 构建 $${GOOS}/$${GOARCH}..." ; \
		GOOS=$${GOOS} GOARCH=$${GOARCH} go build -ldflags "$(LDFLAGS)" -o $${output} $(MAIN_FILE) || exit 1 ; \
	done
	@echo ""
	@echo "$(GREEN)✅ 全部构建完成！$(RESET)"
	@echo ""
	@ls -lh $(BUILD_DIR)/

## 清理构建产物
clean:
	@echo "$(CYAN)🧹 清理构建产物...$(RESET)"
	@rm -rf $(BUILD_DIR)
	@echo "$(GREEN)✅ 清理完成$(RESET)"

## 显示帮助
help:
	@echo "lite-agent 构建工具"
	@echo ""
	@echo "可用命令:"
	@sed -n 's/^## //p' $(MAKEFILE_LIST) | sort | column -t -s ':' | sed 's/^/  make /'

# 禁止将目标名当作文件处理
.DEFAULT_GOAL := help
