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
  drives the indicator and tray, owns the global hotkey. Picks an X11 or Wayland
  backend at startup (`backend.go`); the rest depends only on `platform`.
- `platform` — backend-neutral interfaces (`Hotkey`, `Injector`, `Indicator`) +
  `Mode`, and `Detect` (auto-select from `WAYLAND_DISPLAY` / `XDG_SESSION_TYPE`).
- `inject` — types text via `xdotool` (both backends; on Wayland it reaches
  native windows through Xwayland's XTEST bridge and types Unicode layout-independently).
- `overlay` (X11) — borderless ARGB window near the cursor (status dot + VU meter).
- `hotkey` (X11) — global PTT via `XGrabKey` (reliable release, live Shift = language switch).
- `wayland` — Wayland backend: PTT via the GlobalShortcuts desktop portal (DBus).
  Injection stays on `inject` (xdotool); indicator is tray-only.
- `tray` — StatusNotifier (DBus/SNI) icon: state color + enable/disable toggle.
- `config` — defaults ← `~/.config/vole/config.yaml` ← env overrides.

## Runtime dependencies

Common: `parec` (PulseAudio/PipeWire), `xdotool` (text injection), an SNI tray
host (e.g. lxqt-panel; KDE hosts SNI natively), and the ggml models
(`ggml-large-v3`, `ggml-silero-v5.1.2` for VAD).

- **X11**: a compositor (for overlay transparency).
- **Wayland**: a GlobalShortcuts portal (KWin 5.27+) for PTT, and Xwayland in the
  session so `xdotool` can inject. Selected when `WAYLAND_DISPLAY` /
  `XDG_SESSION_TYPE=wayland` unless `backend` overrides it. No injection fallback:
  on a compositor that does not bridge XTEST to native windows (bare wlroots),
  injection won't reach native clients. The cursor overlay is X11-only (tray
  reflects state on Wayland); a layer-shell overlay is a future addition.

## Conventions

- Keep comments concise and English; document exported identifiers.
- Run `gofmt`/`go vet` before committing.
- X11 calls are not thread-safe: keep each connection driven from a single goroutine.
