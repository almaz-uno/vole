# vole

Offline voice dictation for Linux/X11. A single Go binary runs a resident daemon
that keeps a [whisper.cpp](https://github.com/ggerganov/whisper.cpp) model in
VRAM and types recognized speech into the focused window — push-to-talk: hold the
key, speak, release.

GPU acceleration runs through the **Vulkan** ggml backend, so no CUDA toolkit is
required (works on recent GPUs like Blackwell where a matching CUDA toolkit may
be inconvenient).

## Features

- **Push-to-talk** via a global X11 grab — reliable key release (no "stuck"
  recording), independent of the window manager.
- **On-the-fly language switch**: base language by default, hold **Shift** while
  the key is down to switch to the second language (e.g. `ru` ↔ `en`).
- **Floating indicator** at the cursor: status dot + live VU meter + language label.
- **Tray icon** (StatusNotifier/DBus): shows state by color and toggles
  dictation on/off; the model stays resident for instant resume.
- **VAD** (Silero) + a silence threshold to drop empty/clipped recordings.
- Fast: warm transcription is sub-second per phrase on a mid-range GPU.

## Requirements

- Linux/X11 (developed on i3) with a compositor for overlay transparency.
- `parec` (PulseAudio/PipeWire) and `xdotool`.
- An SNI tray host for the tray icon (e.g. lxqt-panel).
- whisper.cpp built with the Vulkan backend and installed where `pkg-config`
  can find it (`whisper.pc`).
- ggml models (`ggml-large-v3.bin`, `ggml-silero-v5.1.2.bin` for VAD). vole
  fetches them automatically on first run, or via `vole download` — no manual
  step required.

## Build

```sh
make build      # -> .bin/vole
```

`make` exports `PKG_CONFIG_PATH=$HOME/.local/lib/pkgconfig`; CGO links whisper.cpp
via `pkg-config`. The resulting binary is self-contained at runtime (RPATH).

## Usage

```sh
vole daemon                 # resident daemon (model in VRAM, owns the hotkey)
vole start [ru|en|auto]     # start recording (lazily spawns the daemon)
vole stop                   # stop, transcribe, inject
vole download               # fetch missing models (model + VAD)
vole transcribe <wav>       # one-off file transcription (debug)
```

Normally you just run the daemon and use the hotkey. Default binding:
`Super+Ctrl+D` = base language; hold **Shift** for the second language.

## Configuration

Optional `~/.config/vole/config.yaml` (see [`config.yaml.example`](config.yaml.example)).
Any field may be omitted; missing fields fall back to defaults. Environment
variables `VOLE_MODEL` / `VOLE_VAD` / `VOLE_SOCK` override the file.

```yaml
hotkey:
  mods: Super+Control
  key: d
  lang: ru
  lang_shift: en
silence_threshold: 0.005
model: ~/.local/share/dictation/whisper.cpp/models/ggml-large-v3.bin
vad: ~/.local/share/dictation/whisper.cpp/models/ggml-silero-v5.1.2.bin
```

## Autostart (systemd --user)

A user service starts the daemon after the graphical session. Example unit
`~/.config/systemd/user/vole.service`:

```ini
[Unit]
Description=vole — voice dictation
After=graphical-session.target
PartOf=graphical-session.target

[Service]
Environment=DISPLAY=:0
Environment=XAUTHORITY=%h/.Xauthority
ExecStart=%h/path/to/vole/.bin/vole daemon
Restart=on-failure
RestartSec=2

[Install]
WantedBy=graphical-session.target
```

For a bare i3 setup, start it from the WM so the X environment is current:

```
exec --no-startup-id systemctl --user import-environment DISPLAY XAUTHORITY DBUS_SESSION_BUS_ADDRESS && systemctl --user start vole.service
```
