//go:build linux

package inject

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bendahl/uinput"
)

// evdevKeys maps lower-cased xdotool-style key tokens to Linux evdev key codes.
var evdevKeys = map[string]int{
	"shift":   uinput.KeyLeftshift,
	"shift_l": uinput.KeyLeftshift,
	"shift_r": uinput.KeyRightshift,
	"ctrl":    uinput.KeyLeftctrl,
	"control": uinput.KeyLeftctrl,
	"ctrl_l":  uinput.KeyLeftctrl,
	"ctrl_r":  uinput.KeyRightctrl,
	"alt":     uinput.KeyLeftalt,
	"alt_l":   uinput.KeyLeftalt,
	"alt_r":   uinput.KeyRightalt,
	"super":   uinput.KeyLeftmeta,
	"super_l": uinput.KeyLeftmeta,
	"super_r": uinput.KeyRightmeta,
	"meta":    uinput.KeyLeftmeta,
	"insert":  uinput.KeyInsert,
	"delete":  uinput.KeyDelete,
	"v":       uinput.KeyV,
	"c":       uinput.KeyC,
	"x":       uinput.KeyX,
	"z":       uinput.KeyZ,
	"a":       uinput.KeyA,
}

// parseEvdevCombo converts an xdotool key spec (e.g. "shift+Insert", "ctrl+v")
// into a slice of Linux evdev key codes in press order.
// Returns nil if any token is unrecognised.
func parseEvdevCombo(spec string) []int {
	parts := strings.Split(strings.ToLower(spec), "+")
	codes := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		code, ok := evdevKeys[p]
		if !ok {
			return nil
		}
		codes = append(codes, code)
	}
	return codes
}

// openUinputKeyboard opens /dev/uinput, creates a persistent virtual keyboard
// named "vole", and waits for the compositor to enumerate it.
// Returns nil (with a logged reason) if the device cannot be opened.
func openUinputKeyboard(path string) uinput.Keyboard {
	kb, err := uinput.CreateKeyboard(path, []byte("vole"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[vole] inject(paste): uinput unavailable (%v), falling back to xdotool\n", err)
		return nil
	}
	// Give the compositor time to enumerate the new virtual device.
	time.Sleep(300 * time.Millisecond)
	fmt.Fprintf(os.Stderr, "[vole] inject(paste): keystroke via uinput (%s)\n", path)
	return kb
}
