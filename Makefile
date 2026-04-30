SHELL := /bin/sh
APP_NAME := llm-proxy-lite
DIST_DIR ?= dist
GO ?= go
PLATFORM ?= $(shell $(GO) env GOOS)
ARCH ?= $(shell $(GO) env GOARCH)
BIN_NAME ?= $(APP_NAME)-$(PLATFORM)-$(ARCH)
BIN_EXT := $(if $(filter windows win32,$(PLATFORM)),.exe,)
GOOS := $(if $(filter win32,$(PLATFORM)),windows,$(PLATFORM))

.PHONY: build-bin release-bins clean release-all release-linux-amd64 release-linux-arm64 release-darwin-arm64 release-windows-amd64

build-bin:
	mkdir -p $(DIST_DIR)/$(PLATFORM)-$(ARCH)
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(ARCH) $(GO) build -trimpath -ldflags="-s -w" -o $(DIST_DIR)/$(PLATFORM)-$(ARCH)/$(BIN_NAME)$(BIN_EXT) ./cmd/llm-proxy-lite
	@if [ "$(PLATFORM)" = "darwin" ] && [ "$(shell uname)" = "Darwin" ]; then \
		echo "Ad-hoc signing binary for macOS..."; \
		codesign --force --sign - $(DIST_DIR)/$(PLATFORM)-$(ARCH)/$(BIN_NAME)$(BIN_EXT); \
	fi

release-bins: build-bin
	@echo "Built binary in $(DIST_DIR)/$(PLATFORM)-$(ARCH)"

clean:
	rm -rf $(DIST_DIR)

release-all:
	@echo "Run this target in CI matrix per platform/arch"

release-linux-amd64:
	$(MAKE) build-bin PLATFORM=linux ARCH=amd64

release-linux-arm64:
	$(MAKE) build-bin PLATFORM=linux ARCH=arm64

release-darwin-arm64:
	$(MAKE) build-bin PLATFORM=darwin ARCH=arm64

release-windows-amd64:
	$(MAKE) build-bin PLATFORM=win32 ARCH=amd64
