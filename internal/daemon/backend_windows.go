//go:build windows

package daemon

import (
	"fmt"
	"os"

	"github.com/almaz-uno/vole/internal/inject"
	"github.com/almaz-uno/vole/internal/platform"
	"github.com/almaz-uno/vole/internal/winhotkey"
)

// setupIO wires Windows I/O: clipboard+Ctrl+V paste injector and a tray-only
// indicator (no floating overlay).
func (d *Daemon) setupIO(platform.Backend) {
	p := inject.NewPaste(d.cfg.PasteKey, d.cfg.AutoPaste)
	d.inj = p
	d.ind = platform.Nop{}
}

// startHotkey registers Ctrl+Alt+D / Ctrl+Alt+Shift+D (or the configured combo)
// via RegisterHotKey. On error it logs and returns: the daemon keeps working
// without PTT (tray and CLI still work).
func (d *Daemon) startHotkey(platform.Backend) {
	hk, err := winhotkey.New(winhotkey.Config{
		Mods:      d.cfg.Hotkey.Mods,
		Key:       d.cfg.Hotkey.Key,
		LangBase:  d.cfg.Hotkey.Lang,
		LangShift: d.cfg.Hotkey.LangShift,
		OnError: func(err error) {
			notify("🎤 vole", err.Error())
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "vole daemon: hotkey:", err)
		notify("🎤 vole", "hotkey unavailable — dictation via tray/CLI still works")
		return
	}
	d.mu.Lock()
	d.hk = hk
	d.mu.Unlock()

	hk.Listen(
		func(lang string) { d.start(lang, false) },
		func(lang string) { d.setLang(lang) }, // unused: Windows has no live-Shift
		func(lang string) { d.stop(lang) },
	)
}
