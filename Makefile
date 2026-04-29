SHELL := /bin/sh
APP_NAME := llm-proxy-lite
DIST_DIR ?= dist
NODE ?= node
PLATFORM ?= $(shell $(NODE) -p "process.platform")
ARCH ?= $(shell $(NODE) -p "process.arch")
BIN_NAME ?= $(APP_NAME)-$(PLATFORM)-$(ARCH)

.PHONY: build-bin release-bins clean

build-bin:
	BIN_NAME=$(BIN_NAME) BUILD_DIR=$(DIST_DIR)/$(PLATFORM)-$(ARCH) $(NODE) scripts/build-bin.mjs
	@if [ "$(PLATFORM)" = "darwin" ] && [ "$(shell uname)" = "Darwin" ]; then \
		echo "Ad-hoc signing binary for macOS..."; \
		codesign --force --sign - $(DIST_DIR)/$(PLATFORM)-$(ARCH)/$(BIN_NAME); \
	fi

release-bins: build-bin
	@echo "Built binary in $(DIST_DIR)/$(PLATFORM)-$(ARCH)"

clean:
	rm -rf $(DIST_DIR)

release-all:
	@echo "Run this target in CI matrix per platform/arch"

release-linux-amd64:
	$(MAKE) build-bin PLATFORM=linux ARCH=x64

release-linux-arm64:
	$(MAKE) build-bin PLATFORM=linux ARCH=arm64

release-darwin-arm64:
	$(MAKE) build-bin PLATFORM=darwin ARCH=arm64

release-windows-amd64:
	$(MAKE) build-bin PLATFORM=win32 ARCH=x64
