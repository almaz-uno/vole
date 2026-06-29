package inject

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/almaz-uno/vole/internal/platform"
	"github.com/godbus/dbus/v5"
)

// defaultPasteKey is the xdotool key spec used to paste. Shift+Insert pastes in
// most GUI apps; terminals (Konsole) may prefer "ctrl+shift+v".
const defaultPasteKey = "shift+Insert"

// Paster injects text by putting it on the clipboard and synthesizing a paste
// keystroke. Unlike xdotool's keysym typing, this is layout-independent and
// Unicode-correct (the text travels as data, not keycodes), so it works for
// Cyrillic regardless of the active keyboard layout. The clipboard is set
// through the first available of Klipper (KDE), wl-copy (Wayland) or
// xclip/xsel (X11); the keystroke is sent with xdotool (X11 and Xwayland).
type Paster struct {
	setClip   func(text string) error
	clipName  string
	pasteKey  string
	autoPaste bool       // Type() also sends the paste keystroke; false = clipboard only
	conn      *dbus.Conn // kept alive for Klipper; nil otherwise
}

var _ platform.Injector = (*Paster)(nil)

// NewPaste detects a clipboard backend and returns a paste injector. pasteKey is
// the xdotool key spec to paste (empty = default Shift+Insert). autoPaste makes
// Type() paste after copying; when false, Type() only sets the clipboard (the
// user pastes manually) — Insert() always pastes regardless.
func NewPaste(pasteKey string, autoPaste bool) *Paster {
	if pasteKey == "" {
		pasteKey = defaultPasteKey
	}
	p := &Paster{pasteKey: pasteKey, autoPaste: autoPaste}
	p.detectClipboard()
	if p.setClip == nil {
		fmt.Fprintln(os.Stderr, "[vole] inject(paste): no clipboard backend found "+
			"(install one of: KDE Klipper, wl-clipboard, xclip, xsel)")
	} else {
		fmt.Fprintf(os.Stderr, "[vole] inject(paste): clipboard via %s, paste via %q\n", p.clipName, pasteKey)
	}
	return p
}

// detectClipboard picks a clipboard backend, best first.
func (p *Paster) detectClipboard() {
	// 1. KDE Klipper over DBus (works on X11 and Wayland under Plasma).
	if conn, err := dbus.ConnectSessionBus(); err == nil {
		var has bool
		err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.klipper").Store(&has)
		if err == nil && has {
			p.conn, p.clipName = conn, "klipper"
			p.setClip = func(text string) error {
				return conn.Object("org.kde.klipper", "/klipper").
					Call("org.kde.klipper.klipper.setClipboardContents", 0, text).Err
			}
			return
		}
		conn.Close()
	}
	// 2-4. external CLI tools, best first.
	for _, c := range []struct {
		name string
		args []string
	}{
		{"wl-copy", nil},
		{"xclip", []string{"-selection", "clipboard"}},
		{"xsel", []string{"--clipboard", "--input"}},
	} {
		if _, err := exec.LookPath(c.name); err != nil {
			continue
		}
		name, args := c.name, c.args
		p.clipName = name
		p.setClip = func(text string) error { return setClipCLI(name, args, text) }
		return
	}
}

// Type puts text on the clipboard and, if autoPaste is set, pastes it into the
// focused window. With autoPaste off it only sets the clipboard.
func (p *Paster) Type(text string) error {
	return p.put(text, p.autoPaste)
}

// Insert always pastes (used for explicit actions like a history-menu click,
// regardless of the autoPaste setting).
func (p *Paster) Insert(text string) error {
	return p.put(text, true)
}

// Copy puts text on the clipboard without pasting — used for tray-click
// dictation, where the focus is on the tray rather than a text field.
func (p *Paster) Copy(text string) error {
	return p.put(text, false)
}

// Available reports whether a clipboard backend was found.
func (p *Paster) Available() bool { return p.setClip != nil }

func (p *Paster) put(text string, paste bool) error {
	if text == "" {
		return nil
	}
	if p.setClip == nil {
		return fmt.Errorf("inject(paste): no clipboard backend")
	}
	if err := p.setClip(text); err != nil {
		return fmt.Errorf("inject(paste): set clipboard via %s: %w", p.clipName, err)
	}
	if !paste {
		return nil
	}
	out, err := exec.Command("xdotool", "key", "--clearmodifiers", p.pasteKey).CombinedOutput()
	if err != nil {
		return fmt.Errorf("xdotool key %s: %w (%s)", p.pasteKey, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// setClipCLI feeds text to a clipboard tool's stdin. The tool keeps running to
// own the selection (xclip) or daemonizes (wl-copy); either way we do not block
// on it — a background Wait reaps it when it eventually exits.
func setClipCLI(name string, args []string, text string) error {
	cmd := exec.Command(name, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	_, werr := io.WriteString(stdin, text)
	cerr := stdin.Close()
	go func() { _ = cmd.Wait() }()
	if werr != nil {
		return werr
	}
	return cerr
}
