package inject

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"github.com/almaz-uno/vole/internal/platform"
	"github.com/bendahl/uinput"
	"github.com/godbus/dbus/v5"
)

// defaultPasteKey is the xdotool key spec used to paste. Shift+Insert pastes in
// most GUI apps; terminals (Konsole) may prefer "ctrl+shift+v".
const defaultPasteKey = "shift+Insert"

// ownDeadline bounds the read-back wait for the selection-owner handoff: we poll
// until our text is actually served, but never block dictation longer than this.
const ownDeadline = 500 * time.Millisecond

// Paster injects text by putting it on the clipboard and synthesizing a paste
// keystroke. Unlike xdotool's keysym typing, this is layout-independent and
// Unicode-correct (the text travels as data, not keycodes), so it works for
// Cyrillic regardless of the active keyboard layout. The clipboard is set
// through the first available of Klipper (KDE), wl-copy (Wayland) or
// xclip/xsel (X11); the keystroke is sent with xdotool (X11 and Xwayland).
//
// The text is published to *both* X11 selections: CLIPBOARD (read by Ctrl+V /
// Ctrl+Shift+V in GUI apps) and PRIMARY (read by Shift+Insert / middle-click in
// VTE/xterm terminals). Without PRIMARY a Shift+Insert into a terminal would
// re-insert the last mouse selection instead of the dictated text.
type Paster struct {
	setClip    func(text string) error // set CLIPBOARD
	setPrimary func(text string) error // set PRIMARY (nil if the backend can't)
	getClip    func() (string, error)  // read CLIPBOARD back (nil if unreadable)
	getPrimary func() (string, error)  // read PRIMARY back (nil if unreadable)
	clipName   string
	pasteKey   string
	kb         uinput.Keyboard // nil if /dev/uinput is unavailable
	kbKeys     []int           // evdev codes for the paste keystroke; nil = use xdotool
	autoPaste  atomic.Bool // Type() also sends the paste keystroke; false = clipboard only (live: tray toggle)
	conn       *dbus.Conn  // kept alive for Klipper; nil otherwise
}

var (
	_ platform.Injector   = (*Paster)(nil)
	_ platform.AutoPaster = (*Paster)(nil)
)

// NewPaste detects a clipboard backend and returns a paste injector. pasteKey is
// the xdotool key spec to paste (empty = default Shift+Insert). autoPaste makes
// Type() paste after copying; when false, Type() only sets the clipboard (the
// user pastes manually) — Insert() always pastes regardless.
func NewPaste(pasteKey string, autoPaste bool) *Paster {
	if pasteKey == "" {
		pasteKey = defaultPasteKey
	}
	p := &Paster{pasteKey: pasteKey}
	p.autoPaste.Store(autoPaste)
	if codes := parseEvdevCombo(pasteKey); codes != nil {
		p.kbKeys = codes
		p.kb = openUinputKeyboard("/dev/uinput")
	}
	p.detectClipboard()
	if p.setClip == nil {
		fmt.Fprintln(os.Stderr, "[vole] inject(paste): no clipboard backend found "+
			"(install one of: KDE Klipper, wl-clipboard, xclip, xsel)")
	} else {
		prim := ""
		if p.setPrimary != nil {
			prim = " + primary"
		}
		fmt.Fprintf(os.Stderr, "[vole] inject(paste): clipboard%s via %s, paste via %q\n", prim, p.clipName, pasteKey)
	}
	return p
}

// clipTool describes a command-line clipboard backend: how to set and read each
// selection. setBin writes (text on stdin); readBin reads (text on stdout) — the
// same binary for xclip/xsel, but wl-copy reads through its companion wl-paste.
type clipTool struct {
	name    string
	setBin  string
	clipSet []string
	primSet []string
	readBin string
	clipGet []string
	primGet []string
}

// detectCLITool returns the first available command-line clipboard backend, best
// first, or nil if none is installed.
func detectCLITool() *clipTool {
	tools := []clipTool{
		{
			name: "wl-copy", setBin: "wl-copy", clipSet: nil, primSet: []string{"--primary"},
			readBin: "wl-paste", clipGet: []string{"--no-newline"}, primGet: []string{"--primary", "--no-newline"},
		},
		{
			name: "xclip", setBin: "xclip", clipSet: []string{"-selection", "clipboard"}, primSet: []string{"-selection", "primary"},
			readBin: "xclip", clipGet: []string{"-o", "-selection", "clipboard"}, primGet: []string{"-o", "-selection", "primary"},
		},
		{
			name: "xsel", setBin: "xsel", clipSet: []string{"--clipboard", "--input"}, primSet: []string{"--primary", "--input"},
			readBin: "xsel", clipGet: []string{"--clipboard", "--output"}, primGet: []string{"--primary", "--output"},
		},
	}
	for i := range tools {
		if _, err := exec.LookPath(tools[i].setBin); err == nil {
			return &tools[i]
		}
	}
	return nil
}

// detectClipboard picks a clipboard backend, best first.
func (p *Paster) detectClipboard() {
	cli := detectCLITool()

	// 1. KDE Klipper over DBus (works on X11 and Wayland under Plasma). Klipper
	// owns CLIPBOARD only; PRIMARY is driven through a CLI tool when present.
	if conn, err := dbus.ConnectSessionBus(); err == nil {
		var has bool
		err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.klipper").Store(&has)
		if err == nil && has {
			p.conn, p.clipName = conn, "klipper"
			obj := conn.Object("org.kde.klipper", "/klipper")
			p.setClip = func(text string) error {
				return obj.Call("org.kde.klipper.klipper.setClipboardContents", 0, text).Err
			}
			p.getClip = func() (string, error) {
				var s string
				err := obj.Call("org.kde.klipper.klipper.getClipboardContents", 0).Store(&s)
				return s, err
			}
			if cli != nil {
				p.attachPrimary(cli)
			}
			return
		}
		conn.Close()
	}

	// 2. command-line tool (wl-copy / xclip / xsel).
	if cli != nil {
		p.clipName = cli.name
		p.setClip = cliSetter(cli.setBin, cli.clipSet)
		p.getClip = cliGetter(cli.readBin, cli.clipGet)
		p.attachPrimary(cli)
	}
}

// attachPrimary wires PRIMARY get/set from a CLI tool (used by both the CLI
// backend and, for PRIMARY only, the Klipper backend).
func (p *Paster) attachPrimary(c *clipTool) {
	p.setPrimary = cliSetter(c.setBin, c.primSet)
	p.getPrimary = cliGetter(c.readBin, c.primGet)
}

// cliSetter returns a func that feeds text to a clipboard tool's stdin.
func cliSetter(bin string, args []string) func(string) error {
	return func(text string) error { return setClipCLI(bin, args, text) }
}

// cliGetter returns a func that reads a selection's contents, or nil if the
// reader binary is not installed (so the read-back wait degrades to a settle).
func cliGetter(bin string, args []string) func() (string, error) {
	if _, err := exec.LookPath(bin); err != nil {
		return nil
	}
	return func() (string, error) {
		out, err := exec.Command(bin, args...).Output()
		return string(out), err
	}
}

// Type puts text on the clipboard and, if autoPaste is set, pastes it into the
// focused window. With autoPaste off it only sets the clipboard.
func (p *Paster) Type(text string) error {
	return p.put(text, p.autoPaste.Load())
}

// AutoPaste reports whether Type() pastes after copying (vs. clipboard only).
func (p *Paster) AutoPaste() bool { return p.autoPaste.Load() }

// SetAutoPaste flips the auto-paste behavior at runtime (tray toggle).
func (p *Paster) SetAutoPaste(v bool) { p.autoPaste.Store(v) }

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
	// Also own PRIMARY: VTE/xterm terminals paste PRIMARY on Shift+Insert, not
	// CLIPBOARD; without this they'd re-insert the last mouse selection.
	if p.setPrimary != nil {
		if err := p.setPrimary(text); err != nil {
			fmt.Fprintf(os.Stderr, "[vole] inject(paste): set primary: %v\n", err)
		}
	}
	if !paste {
		return nil
	}
	// The X11 clipboard is an ownership protocol: the CLI tool acquires the
	// selection asynchronously, so a keystroke sent immediately can be served by
	// the previous owner (stale content). Wait until our text is actually served
	// before pasting.
	p.waitOwned(text)
	// Use uinput when available: events go through the kernel input stack directly
	// to the compositor, so modifier state never leaks into the Wayland session.
	// Fall back to xdotool (XWayland path) when /dev/uinput is inaccessible.
	if p.kb != nil {
		return p.sendKeystroke()
	}
	out, err := exec.Command("xdotool",
		"keyup",
		"Control_L", "Control_R", "Super_L", "Super_R",
		"Alt_L", "Alt_R", "Shift_L", "Shift_R",
		"key", p.pasteKey,
		"keyup", "Shift_L", "Shift_R").CombinedOutput()
	if err != nil {
		return fmt.Errorf("xdotool key %s: %w (%s)", p.pasteKey, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// sendKeystroke sends the paste keystroke via the uinput virtual keyboard.
// Keys are pressed in order and released in reverse (modifier → key → key → modifier).
func (p *Paster) sendKeystroke() error {
	for _, code := range p.kbKeys {
		if err := p.kb.KeyDown(code); err != nil {
			return fmt.Errorf("uinput key down %d: %w", code, err)
		}
	}
	for i := len(p.kbKeys) - 1; i >= 0; i-- {
		if err := p.kb.KeyUp(p.kbKeys[i]); err != nil {
			return fmt.Errorf("uinput key up %d: %w", p.kbKeys[i], err)
		}
	}
	return nil
}

// Close releases the uinput keyboard device (if open) and the D-Bus connection.
func (p *Paster) Close() {
	if p.kb != nil {
		_ = p.kb.Close()
		p.kb = nil
	}
	if p.conn != nil {
		p.conn.Close()
		p.conn = nil
	}
}

// waitOwned polls the readable selections until each serves text — i.e. the
// selection-owner handoff has completed and a paste will see our content. It
// returns on the first all-match or when ownDeadline elapses (so a flaky reader
// never blocks dictation); with no reader it falls back to a short settle.
func (p *Paster) waitOwned(text string) {
	readers := make([]func() (string, error), 0, 2)
	if p.getClip != nil {
		readers = append(readers, p.getClip)
	}
	if p.getPrimary != nil {
		readers = append(readers, p.getPrimary)
	}
	if len(readers) == 0 {
		time.Sleep(40 * time.Millisecond)
		return
	}
	deadline := time.Now().Add(ownDeadline)
	for {
		ok := true
		for _, r := range readers {
			if got, err := r(); err != nil || got != text {
				ok = false
				break
			}
		}
		if ok || time.Now().After(deadline) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
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
