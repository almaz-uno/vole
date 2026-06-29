// Package platform abstracts vole's input/output surface so the daemon is
// backend-agnostic. Two backends satisfy these interfaces:
//
//   - X11: XGrabKey hotkey, xdotool injection, ARGB override-redirect overlay
//     (internal/{hotkey,inject,overlay}).
//   - Wayland: GlobalShortcuts desktop portal, ydotool/wtype/wl-clipboard
//     injection, tray-only indicator (internal/wayland).
//
// internal/daemon depends only on the interfaces here; it picks a backend at
// runtime via Detect.
package platform

import (
	"os"
	"strings"
)

// Mode is the indicator state.
type Mode int

const (
	ModeHidden Mode = iota
	ModeRecording
	ModeProcessing
	ModeDownloading
	ModeToast
)

// Hotkey is the global push-to-talk source. Listen blocks until Close, invoking
// onStart on press (with the language id), onLang when the language changes
// mid-recording (X11 live Shift; not emitted by backends that cannot observe
// it), and onStop on release (with the final language).
type Hotkey interface {
	Listen(onStart, onLang, onStop func(lang string))
	Close()
}

// Injector types transcribed text into the focused window.
type Injector interface {
	Type(text string) error
}

// Inserter is an Injector that can force-insert text even when auto-paste is
// off — used for explicit actions such as clicking a history entry.
type Inserter interface {
	Injector
	Insert(text string) error
}

// Copier is an Injector that can put text on the clipboard without pasting —
// used for tray-click dictation, where the focus is on the tray, not a field.
type Copier interface {
	Injector
	Copy(text string) error
}

// Indicator is the floating recording/transcribing/download indicator. A
// backend without a visible overlay (Wayland tray-only) uses Nop.
type Indicator interface {
	Show(lang string)
	SetLevel(float64)
	SetLang(string)
	SetMode(Mode)
	ShowDownload(label string)
	SetProgress(frac float64)
	// Toast shows a brief, self-dismissing confirmation (e.g. "copied to
	// clipboard") after a tray-click dictation. A backend without an overlay
	// (Nop) ignores it; the daemon then falls back to a desktop notification.
	Toast(text string)
	Hide()
	Close()
}

// Backend identifies an input/output backend.
type Backend string

const (
	BackendAuto    Backend = "auto"
	BackendX11     Backend = "x11"
	BackendWayland Backend = "wayland"
)

// Detect resolves a configured backend to a concrete one. "auto" (or any
// unknown value) prefers Wayland when WAYLAND_DISPLAY is set or
// XDG_SESSION_TYPE=wayland, otherwise X11.
func Detect(b Backend) Backend {
	switch b {
	case BackendX11, BackendWayland:
		return b
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" ||
		strings.EqualFold(os.Getenv("XDG_SESSION_TYPE"), "wayland") {
		return BackendWayland
	}
	return BackendX11
}

// Nop is an Indicator that draws nothing — used on backends that rely on the
// tray alone (Wayland, until a layer-shell overlay lands), or when no display
// is available.
type Nop struct{}

func (Nop) Show(string)         {}
func (Nop) SetLevel(float64)    {}
func (Nop) SetLang(string)      {}
func (Nop) SetMode(Mode)        {}
func (Nop) ShowDownload(string) {}
func (Nop) SetProgress(float64) {}
func (Nop) Toast(string)        {}
func (Nop) Hide()               {}
func (Nop) Close()              {}

var _ Indicator = Nop{}
