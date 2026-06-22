# vole — voice dictation via whisper.cpp (GPU/Vulkan)
#
# whisper.cpp must be installed into a prefix visible to pkg-config (see
# whisper.pc). PKG_CONFIG_PATH below points cgo at it.

export PKG_CONFIG_PATH := $(HOME)/.local/lib/pkgconfig

BIN := .bin/vole

.PHONY: all build run-daemon vet clean

all: build

build:
	go build -o $(BIN) ./cmd/vole

vet:
	go vet ./...

run-daemon: build
	./$(BIN) daemon

clean:
	rm -rf .bin
