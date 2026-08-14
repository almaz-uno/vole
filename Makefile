# vole — voice dictation via whisper.cpp (GPU/Vulkan)
#
# Linux: whisper.cpp must be installed into a prefix visible to pkg-config (see
# whisper.pc). PKG_CONFIG_PATH below points cgo at it.
#
# Windows: use scripts/build-whisper-windows.ps1 then scripts/build-windows.ps1
# (MinGW gcc + Vulkan SDK). Do not use this Makefile on Windows.

export PKG_CONFIG_PATH := $(HOME)/.local/lib/pkgconfig

BIN := .bin/vole

# Version from git (latest tag + commits since + -dirty); "dev" outside a checkout.
# Release builds override this via -ldflags in the CI workflow (GITHUB_REF_NAME).
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: all build run-daemon vet clean

all: build

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BIN) ./cmd/vole

vet:
	go vet ./...

run-daemon: build
	./$(BIN) daemon

clean:
	rm -rf .bin
