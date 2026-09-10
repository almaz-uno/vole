//go:build linux

// Package inject types the transcribed text into the active window via xdotool.
// It is used on both backends: on X11 directly, and on a Wayland session through
// Xwayland's XTEST bridge (KWin/Mutter route emulated input to the focused
// native window). Because xdotool types keysyms (not raw keycodes), it produces
// Unicode — Cyrillic included — regardless of the active keyboard layout, and it
// works in terminals (it types, it does not paste).
package inject

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/almaz-uno/vole/internal/platform"
)

// Injector is the xdotool text injector. It satisfies platform.Injector.
type Injector struct{}

var _ platform.Injector = Injector{}

// New returns the xdotool injector.
func New() Injector { return Injector{} }

// Type types text into the active window via xdotool.
func (Injector) Type(text string) error { return Type(text) }

// Insert is the same as Type for the xdotool injector (it always inserts);
// it exists so the daemon can force insertion on a history-menu click.
func (Injector) Insert(text string) error { return Type(text) }

// Type types text into the active window via xdotool.
// The text is passed over stdin (--file -) to avoid argument-length limits
// and shell quoting issues with special characters.
func Type(text string) error {
	if text == "" {
		return nil
	}
	// Release modifiers left "stuck" after the PTT hotkey ($mod+Ctrl+…):
	// otherwise the first letters fire as Ctrl+W/Ctrl+Q and close the window.
	_ = exec.Command("xdotool", "keyup",
		"Control_L", "Control_R", "Super_L", "Super_R",
		"Alt_L", "Alt_R", "Shift_L", "Shift_R").Run()

	cmd := exec.Command("xdotool", "type", "--clearmodifiers", "--delay", "4", "--file", "-")
	cmd.Stdin = strings.NewReader(text)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("xdotool type: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
