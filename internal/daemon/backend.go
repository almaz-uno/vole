package daemon

import (
	"fmt"
	"os"

	"github.com/almaz-uno/vole/internal/hotkey"
	"github.com/almaz-uno/vole/internal/inject"
	"github.com/almaz-uno/vole/internal/overlay"
	"github.com/almaz-uno/vole/internal/platform"
	"github.com/almaz-uno/vole/internal/wayland"
)

// setupIO selects the indicator and injector for the backend. Both fields are
// always set (the indicator falls back to a no-op so the rest of the daemon
// needs no nil checks).
func (d *Daemon) setupIO(backend platform.Backend) {
	// Injection is xdotool on both backends. On a KWin/Mutter Wayland session it
	// reaches native windows through Xwayland's XTEST bridge, and — unlike raw
	// uinput — it types Unicode regardless of the active keyboard layout, so
	// Cyrillic and terminals work. If xdotool can't reach (e.g. bare wlroots, or
	// no Xwayland), injection simply does nothing; there is no fallback.
	d.inj = inject.New()

	if backend == platform.BackendWayland {
		// Tray-only first cut: the SNI tray already shows state and is
		// Wayland-native; a layer-shell overlay can replace Nop later.
		d.ind = platform.Nop{}
		return
	}
	if ov, err := overlay.New(); err != nil {
		fmt.Fprintln(os.Stderr, "vole daemon: overlay unavailable:", err)
		d.ind = platform.Nop{}
	} else {
		d.ind = ov
	}
}

// startHotkey constructs and runs the PTT hotkey for the backend. It blocks in
// Listen, so it is meant to run in its own goroutine. On error (no X grab, no
// portal) it logs and returns: the daemon keeps working, just without PTT.
func (d *Daemon) startHotkey(backend platform.Backend) {
	var (
		hk  platform.Hotkey
		err error
	)
	switch backend {
	case platform.BackendWayland:
		hk, err = wayland.NewHotkey(wayland.HotkeyConfig{
			LangBase:  d.cfg.Hotkey.Lang,
			LangShift: d.cfg.Hotkey.LangShift,
		})
	default: // X11 — the daemon grabs the key itself (reliable release + live Shift)
		hk, err = hotkey.New(hotkey.Config{
			Mods:      d.cfg.Hotkey.Mods,
			Key:       d.cfg.Hotkey.Key,
			LangBase:  d.cfg.Hotkey.Lang,
			LangShift: d.cfg.Hotkey.LangShift,
		})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "vole daemon: hotkey:", err)
		return
	}
	d.mu.Lock()
	d.hk = hk
	d.mu.Unlock()

	hk.Listen(
		func(lang string) { d.start(lang) },   // PTT press
		func(lang string) { d.setLang(lang) }, // live Shift (X11 only)
		func(lang string) { d.stop(lang) },    // release (final language)
	)
}
