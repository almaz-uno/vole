# vole

Offline voice dictation for Linux (X11 and Wayland) and Windows. A single Go binary runs a
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
- **Pluggable backend**: X11, Wayland, and Windows implementations behind one
  interface, selected automatically from the OS/session (override with
  `backend:` / `VOLE_BACKEND`).
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
- `xdotool` (X11: types the text; Wayland: sends the paste keystroke).
- An SNI tray host for the tray icon (e.g. lxqt-panel; KDE Plasma hosts SNI natively).
- **X11**: a compositor for overlay transparency.
- **Wayland**: a GlobalShortcuts portal (KWin/Plasma 5.27+) for push-to-talk;
  KDE Klipper for the clipboard; and Xwayland for the paste keystroke.
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
(alternate language) — and you assign the actual keys in **System Settings →
Shortcuts**. On first run KDE shows a consent dialog ("allow this app to register
global shortcuts") — confirm it, and the two shortcuts then appear under
Shortcuts. The `mods`/`key` config seeds a *suggested* trigger: `dictate` gets
`mods+key`, `dictate-alt` gets `mods+Shift+key` (e.g. `mods: Super, key: k` →
`Meta+K` / `Meta+Shift+K`); KDE may pre-fill it, and you can still override. The
portal reports press/release, so push-to-talk works; it cannot observe a live
Shift, so the two languages are separate keys rather than a Shift toggle.

**Text injection** goes through the clipboard. `xdotool type` is *not* usable on
KWin Wayland: XTEST sends keycodes that KWin re-interprets through the active
keyboard layout, so dictating Russian under a US layout types transliterated
Latin. Instead vole puts the text on the clipboard (KDE Klipper over DBus —
layout-independent, correct for any Unicode) and pastes it with a keystroke
(`paste_key`, default **Shift+Insert**; terminals may want `ctrl+shift+v`). This
needs KDE's Klipper (present on Plasma) and Xwayland for the paste keystroke.

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

## Windows

Windows 10/11 with a **working Vulkan GPU driver**. There is no floating overlay
and no live-Shift language switch; status lives in the **system tray** (notification
area, including the overflow `^`). Injection is always Unicode clipboard + **Ctrl+V**.

This is a user-session tray app, **not** a Windows Service (`services.msc`). A
service would run in Session 0 and would not own a tray icon or paste into the
interactive desktop.

### Requirements (Windows)

- Windows 10/11, microphone, GPU + Vulkan driver (vendor package, not the SDK).
- MinGW-w64 **gcc** (CGO), CMake, Ninja (optional), [Vulkan SDK](https://vulkan.lunarg.com/) to *build* whisper.cpp.
- Runtime DLLs next to `vole.exe`: `whisper.dll`, `ggml*.dll`, `vulkan-1.dll` (copied by the build script).

CPU-only whisper is a developer fallback, not a supported release.

### Build (Windows)

whisper.cpp is linked through a **Windows-specific** cgo path (`internal/whisper/cgo_windows.go`).
Linux `pkg-config` / `~/.local` is not used.

```powershell
# 1. Vulkan SDK installed; $env:VULKAN_SDK set (reopen the shell after install)
# 2. gcc, g++, cmake on PATH (MSYS2 mingw64 or chocolatey mingw)

.\scripts\build-whisper-windows.ps1   # -> deps/whisper (headers, import libs, DLLs)
.\scripts\build-windows.ps1           # -> .bin/vole.exe + DLLs
```

`CGO_CFLAGS` / `CGO_LDFLAGS` can override the default `-Ideps/whisper/include` and
`-Ldeps/whisper/lib … -lwhisper -lggml -lggml-base -lggml-cpu -lggml-vulkan -lvulkan-1`.

Working directory at runtime **must** be the folder that contains `vole.exe` and
the DLLs (Task Scheduler “Start in”).

### Vulkan log marker

On a successful GPU load, stderr contains ggml/whisper Vulkan lines, for example:

```
ggml_vulkan: Found 1 Vulkan devices:
[vole] whisper: ... VULKAN = 1 ...
```

`vole transcribe file.wav` prints the same marker (CLI stdout/stderr stay attached;
only the `daemon` process may hide *its own* console). If the driver is missing,
model load fails with an explicit error — silent CPU fallback is not a success.

### Configuring vole on Windows

Config file: `%AppData%\vole\config.yaml` (create the directory if needed).
Copy comments from [`config.yaml.example`](config.yaml.example). **Restart the
daemon after editing the yaml.** Environment overrides: `VOLE_MODEL`, `VOLE_VAD`,
`VOLE_SOCK`, `VOLE_BACKEND`, `VOLE_RECORD`, `VOLE_POSTPROCESS`.

| Setting | Default | Where to change |
|---------|---------|-----------------|
| `backend` | `auto` → `windows` | yaml / `VOLE_BACKEND` |
| `inject` / `paste_key` | `paste` / `ctrl+v` | yaml only |
| `auto_paste` | `true` | yaml + **tray checkbox** |
| `hotkey.mods` / `key` | `Control+Alt` / `d` | yaml only (restart) |
| `hotkey.lang` / `lang_shift` | `ru` / `en` | yaml only (restart) |
| `model` / `vad` | `%LocalAppData%\vole\models\…` | yaml / `VOLE_MODEL` / `VOLE_VAD` |
| `postprocess` | empty | yaml / `VOLE_POSTPROCESS` |
| `postprocess_on` | `false` | yaml + **tray checkbox** (only if a path is set) |
| `english_input` | `false` | yaml + tray checkbox |

IPC: named pipe `\\.\pipe\vole` (`VOLE_SOCK` to override). History:
`%LocalAppData%\vole\history.jsonl`. Models: `vole download` (or first daemon
start) writes `%LocalAppData%\vole\models\`.

**Default hotkeys:** hold **Ctrl+Alt+D** (Russian) or **Ctrl+Alt+Shift+D** (English).
Change `hotkey.mods` / `key` in yaml and restart. There is no live-Shift: the
Shift combo is a second `RegisterHotKey`.

**Known shortcut conflicts** (do not use these as defaults):

- `Win+Ctrl+D` — Windows virtual desktop
- `Win+H` — Windows voice typing
- `Win+Shift+S` — Snipping Tool (if you bind Shift+Win)

If `RegisterHotKey` fails because another app owns the combo, vole logs the error
(and shows a notification) and **keeps running** without PTT; tray and
`vole start` / `vole stop` still work. Pick another combo in yaml.

**Post-process:** set `postprocess` to an executable, or a command line run
directly (no shell):

```yaml
postprocess: powershell.exe -NoProfile -File C:\Users\you\vole-post.ps1
postprocess_on: true
```

The script reads the transcript on stdin and writes the improved text on stdout.
On error, timeout, or empty stdout vole pastes the raw transcript. A hung script
is killed via a **Windows job object** (the whole process tree, including
children of `powershell.exe`). Toggle at runtime with the tray **Post-process**
checkbox (hidden when no path is configured).

### Tray

`vole daemon` is a tray application. The icon sits in the notification area
(bottom-right; it may land in the overflow `^` — pin it via the taskbar
overflow settings). Right-click opens a context menu (same idea as the system
Sound icon): Enable/Disable dictation, Auto-paste, Post-process, English input,
Recent dictations, Quit. Left-click start/stop is supported when the tray host
delivers it. Colors: idle / recording (red) / transcribing (amber) /
post-process (blue).

A console window is **not** required. If you start vole from `cmd.exe`, that
console stays so you can read Vulkan logs. Double-click / Task Scheduler hide
vole’s own console.

### Autostart

**Recommended — Task Scheduler** (current user, after logon):

1. Task Scheduler → Create Task (not a basic task).
2. General: name `vole`; **Run only when user is logged on** (needed for the tray).
3. Triggers: **At log on** → specific user (you).
4. Actions: Start a program → `C:\path\to\.bin\vole.exe` with arguments `daemon`.
5. **Start in** (working directory): `C:\path\to\.bin` — the folder with `vole.exe` **and** the DLLs.
6. Do **not** choose “Run whether user is logged on or not”.

**Alternative — Startup folder:** `Win+R` → `shell:startup` → shortcut to
`vole.exe daemon`, “Start in” = the `.bin` folder.

Do **not** install vole as a Windows Service.
