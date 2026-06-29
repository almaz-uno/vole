package daemon

import (
	"fmt"
	"os"
	"strings"

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
	// Injection method is independent of the backend (config: auto|type|paste).
	// "type" = xdotool (only correct on a real X server). "paste" = clipboard +
	// a paste keystroke (layout-independent, the only option that types Cyrillic
	// correctly on KWin Wayland). "auto" = type on X11, paste on Wayland.
	if injectIsPaste(d.cfg.Inject, backend) {
		if p := inject.NewPaste(d.cfg.PasteKey, d.cfg.AutoPaste); p.Available() {
			d.inj = p
		} else {
			fmt.Fprintln(os.Stderr, "vole daemon: no clipboard backend — falling back to xdotool typing")
			d.inj = inject.New()
		}
	} else {
		d.inj = inject.New()
	}

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

// injectIsPaste resolves the configured injection method to paste (true) or
// type (false). "auto" pastes on Wayland and types on X11.
func injectIsPaste(method string, backend platform.Backend) bool {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "paste":
		return true
	case "type":
		return false
	default: // auto
		return backend == platform.BackendWayland
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
			Mods:      d.cfg.Hotkey.Mods, // seeds the portal's suggested trigger
			Key:       d.cfg.Hotkey.Key,
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
		func(lang string) { d.start(lang, false) }, // PTT press
		func(lang string) { d.setLang(lang) },      // live Shift (X11 only)
		func(lang string) { d.stop(lang) },         // release (final language)
	)
}
