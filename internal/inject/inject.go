// Package inject types the transcribed text into the active window.
package inject

import (
	"fmt"
	"os/exec"
	"strings"
)

// Type types text into the active window via xdotool (X11).
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
