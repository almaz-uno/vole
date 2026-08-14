# Agent Instructions

## Project Language

The primary language for this project is **English**.

All code, documentation, comments, string literals, commit messages, and
in-repo communication must be in English. (Chat with the maintainer may be in
Russian, but repository artifacts stay English.)

## What vole is

vole is an offline voice-dictation tool for Linux (X11/Wayland) and Windows. A
single Go binary runs a resident daemon that keeps the whisper model in VRAM
and types recognized speech into the focused window. Input is push-to-talk:
hold the hotkey, speak, release — the text is transcribed and injected.

## Build

**Linux:** whisper.cpp must be installed into a prefix visible to `pkg-config`
(the repo assumes `~/.local`, with a corrected `whisper.pc`). Then:

```sh
make build        # exports PKG_CONFIG_PATH=$HOME/.local/lib/pkgconfig, builds .bin/vole
```

- CGO links whisper.cpp via `pkg-config --libs whisper`.
- GPU runs through the **Vulkan** ggml backend (no CUDA toolkit required).
- The binary is self-contained at runtime (RPATH to `~/.local/lib`).

**Windows:** separate whisper.cpp+Vulkan build (no pkg-config). See README
“Build (Windows)” and `scripts/build-whisper-windows.ps1` /
`scripts/build-windows.ps1`. CGO flags live in `internal/whisper/cgo_windows.go`.
Do not fold Windows ld-flags into the Linux cgo file.

gopls on Linux needs the same env; `.vscode/settings.json` sets `PKG_CONFIG_PATH`
so the language server resolves the cgo package.

## Architecture (`internal/`)

- `whisper` — thin cgo wrapper over whisper.cpp (model load, transcribe).
  Linux: `pkg-config`. Windows: `*_windows.go` + `deps/whisper`.
- `audio` — microphone capture (Linux `parec`, Windows WASAPI) and WAV decode.
- `daemon` — resident server: unix socket or Windows named pipe, orchestrates
  capture/transcribe/inject, drives the indicator and tray, owns the global
  hotkey. Picks an X11, Wayland, or Windows backend at startup; the rest depends
  only on `platform`.
- `platform` — backend-neutral interfaces (`Hotkey`, `Injector`, `Indicator`) +
  `Mode`, and `Detect` (Windows via `GOOS`; else Wayland vs X11 from
  `WAYLAND_DISPLAY` / `XDG_SESSION_TYPE`).
- `inject` — Linux: xdotool / clipboard+keystroke. Windows: Win32 clipboard +
  SendInput Ctrl+V.
- `overlay` (X11) — borderless ARGB window near the cursor (status dot + VU meter).
- `hotkey` (X11) — global PTT via `XGrabKey` (reliable release, live Shift = language switch).
- `wayland` — Wayland backend: PTT via the GlobalShortcuts desktop portal (DBus).
  Injection stays on `inject` (xdotool); indicator is tray-only.
- `winhotkey` — Windows PTT via `RegisterHotKey` (press + GetAsyncKeyState release;
  no live-Shift).
- `ipc` — unix socket (Linux) or named pipe `\\.\pipe\vole` (Windows).
- `tray` — fyne.io/systray icon: state color + enable/disable toggle (SNI on
  Linux, NotifyIcon on Windows).
- `config` — defaults ← config.yaml ← env overrides. Linux:
  `~/.config/vole/config.yaml`. Windows: `%AppData%\vole\config.yaml`.

## Runtime dependencies

**Linux:** `parec` (PulseAudio/PipeWire), `xdotool` (text injection), an SNI tray
host (e.g. lxqt-panel; KDE hosts SNI natively), and the ggml models
(`ggml-large-v3`, `ggml-silero-v5.1.2` for VAD).

- **X11**: a compositor (for overlay transparency).
- **Wayland**: a GlobalShortcuts portal (KWin 5.27+) for PTT, and Xwayland in the
  session so `xdotool` can inject. Selected when `WAYLAND_DISPLAY` /
  `XDG_SESSION_TYPE=wayland` unless `backend` overrides it. No injection fallback:
  on a compositor that does not bridge XTEST to native windows (bare wlroots),
  injection won't reach native clients. The cursor overlay is X11-only (tray
  reflects state on Wayland); a layer-shell overlay is a future addition.

**Windows:** WASAPI capture, Win32 clipboard+SendInput, system tray. Vulkan GPU
driver at runtime; whisper/ggml/vulkan DLLs next to `vole.exe`. No overlay.
Autostart via Task Scheduler (At log on) or the Startup folder — not a Windows
Service.

## Conventions

- Keep comments concise and English; document exported identifiers.
- Run `gofmt`/`go vet` before committing.
- X11 calls are not thread-safe: keep each connection driven from a single goroutine.
- Windows-only code uses `//go:build windows` (or `_windows.go`); do not break
  the Linux pkg-config whisper path.
