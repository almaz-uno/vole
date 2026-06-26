# vole

Offline voice dictation for Linux (X11 and Wayland). A single Go binary runs a
resident daemon that keeps a [whisper.cpp](https://github.com/ggerganov/whisper.cpp)
model in VRAM and types recognized speech into the focused window — push-to-talk:
hold the key, speak, release.

GPU acceleration runs through the **Vulkan** ggml backend, so no CUDA toolkit is
required (works on recent GPUs like Blackwell where a matching CUDA toolkit may
be inconvenient).

## Features

- **Push-to-talk** independent of the window manager: a global X11 grab with
  reliable key release (no "stuck" recording), or the Wayland GlobalShortcuts
  desktop portal.
- **On-the-fly language switch** (X11): base language by default, hold **Shift**
  while the key is down to switch to the second language (e.g. `ru` ↔ `en`). On
  Wayland the two languages are separate, individually-bound shortcuts.
- **Pluggable backend**: X11 and Wayland implementations behind one interface,
  selected automatically from the session (override with `backend:` / `VOLE_BACKEND`).
- **Floating indicator** at the cursor (X11): status dot + live VU meter +
  language label. On Wayland the tray reflects state (a layer-shell overlay may
  come later).
- **Tray icon** (StatusNotifier/DBus): shows state by color and toggles
  dictation on/off; the model stays resident for instant resume.
- **VAD** (Silero) + a silence threshold to drop empty/clipped recordings.
- Fast: warm transcription is sub-second per phrase on a mid-range GPU.

## Requirements

- Linux, X11 or Wayland.
- `parec` (PulseAudio/PipeWire) for capture.
- `xdotool` for text injection (on both backends — see [Wayland](#wayland)).
- An SNI tray host for the tray icon (e.g. lxqt-panel; KDE Plasma hosts SNI natively).
- **X11**: a compositor for overlay transparency.
- **Wayland**: a GlobalShortcuts portal (KWin/Plasma 5.27+) for push-to-talk, and
  Xwayland (any KDE/GNOME Wayland session has it) so `xdotool` can inject.
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
variables `VOLE_MODEL` / `VOLE_VAD` / `VOLE_SOCK` / `VOLE_BACKEND` override the file.

```yaml
backend: auto         # auto | x11 | wayland
hotkey:
  mods: Super+Control
  key: d
  lang: ru
  lang_shift: en
silence_threshold: 0.005
model: ~/.local/share/dictation/whisper.cpp/models/ggml-large-v3.bin
vad: ~/.local/share/dictation/whisper.cpp/models/ggml-silero-v5.1.2.bin
```

## Wayland

Under a native Wayland session (e.g. KDE Plasma on `kwin_wayland`), vole switches
to the Wayland backend automatically. Two pieces differ from X11:

**Push-to-talk** uses the `org.freedesktop.portal.GlobalShortcuts` portal. vole
registers two shortcut *ids* — `dictate` (base language) and `dictate-alt`
(alternate language) — and you bind the actual keys in **System Settings →
Shortcuts** (the `mods`/`key` config fields do not apply). The portal reports
press/release, so push-to-talk works; it cannot observe a live Shift, so the two
languages are separate keys rather than a Shift toggle.

**Text injection** stays on `xdotool`. On KWin/Mutter, `xdotool type` reaches the
focused window — including native Wayland ones — through Xwayland's XTEST bridge,
and because it types keysyms it produces Cyrillic (and any Unicode) regardless of
the active layout and works in terminals. This needs Xwayland in the session
(every KDE/GNOME Wayland session has it). There is **no fallback**: on a
compositor that does not bridge XTEST to native windows (e.g. bare wlroots —
sway/Hyprland), injection won't reach native Wayland clients.

The floating cursor overlay is X11-only for now; on Wayland the tray icon
reflects recording/transcribing state.

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
