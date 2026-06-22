# Agent Instructions

## Project Language

The primary language for this project is **English**.

All code, documentation, comments, string literals, commit messages, and
in-repo communication must be in English. (Chat with the maintainer may be in
Russian, but repository artifacts stay English.)

## What vole is

vole is an offline voice-dictation tool for Linux/X11 (tested with i3). A single
Go binary runs a resident daemon that keeps the whisper model in VRAM and types
recognized speech into the focused window. Input is push-to-talk: hold the
hotkey, speak, release — the text is transcribed and injected.

## Build

whisper.cpp must be installed into a prefix visible to `pkg-config` (the repo
assumes `~/.local`, with a corrected `whisper.pc`). Then:

```sh
make build        # exports PKG_CONFIG_PATH=$HOME/.local/lib/pkgconfig, builds .bin/vole
```

- CGO links whisper.cpp via `pkg-config --libs whisper`.
- GPU runs through the **Vulkan** ggml backend (no CUDA toolkit required).
- The binary is self-contained at runtime (RPATH to `~/.local/lib`).

gopls needs the same env; `.vscode/settings.json` sets `PKG_CONFIG_PATH` so the
language server resolves the cgo package.

## Architecture (`internal/`)

- `whisper` — thin cgo wrapper over whisper.cpp (model load, transcribe).
- `audio` — microphone capture via `parec` (streamed PCM + RMS level) and WAV decode.
- `daemon` — resident server: unix socket, orchestrates capture/transcribe/inject,
  drives overlay and tray, owns the global hotkey.
- `inject` — types text into the focused window via `xdotool`.
- `overlay` — borderless ARGB X11 window near the cursor (status dot + VU meter).
- `hotkey` — global PTT via X11 `XGrabKey` (reliable release, live Shift = language switch).
- `tray` — StatusNotifier (DBus/SNI) icon: state color + enable/disable toggle.
- `config` — defaults ← `~/.config/vole/config.yaml` ← env overrides.

## Runtime dependencies

`parec` (PulseAudio/PipeWire), `xdotool`, an X11 compositor (for overlay
transparency), an SNI tray host (e.g. lxqt-panel), and the ggml models
(`ggml-large-v3`, `ggml-silero-v5.1.2` for VAD).

## Conventions

- Keep comments concise and English; document exported identifiers.
- Run `gofmt`/`go vet` before committing.
- X11 calls are not thread-safe: keep each connection driven from a single goroutine.
