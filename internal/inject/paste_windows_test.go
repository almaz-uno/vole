//go:build windows

package inject

import "testing"

func TestParsePasteKey(t *testing.T) {
	cases := []struct {
		in   string
		want []uint16
	}{
		{"ctrl+v", []uint16{vkLControl, 0x56}},
		{"Control+V", []uint16{vkLControl, 0x56}},
		{"shift+insert", []uint16{vkShift, vkInsert}},
		{"ctrl+shift+v", []uint16{vkLControl, vkShift, 0x56}},
	}
	for _, c := range cases {
		got := parsePasteKey(c.in)
		if len(got) != len(c.want) {
			t.Errorf("parsePasteKey(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("parsePasteKey(%q) = %v, want %v", c.in, got, c.want)
				break
			}
		}
	}
}

func TestUseUnicodeInputOnlyForUnicodeMode(t *testing.T) {
	if !useUnicodeInput("unicode") {
		t.Fatal("unicode mode should use direct Unicode input")
	}
	for _, spec := range []string{"ctrl+v", "shift+insert", "ctrl+shift+v"} {
		if useUnicodeInput(spec) {
			t.Errorf("%q should use its configured key combination", spec)
		}
	}
}

func TestRightShiftUsesDedicatedNonExtendedScanCode(t *testing.T) {
	if got := vkScan(vkRShift); got != 0x36 {
		t.Fatalf("vkScan(VK_RSHIFT) = %#x, want 0x36", got)
	}
	if got := keyFlags(vkRShift, true); got&keyeventfExtended != 0 {
		t.Fatalf("Right Shift key-up flags %#x must not include EXTENDEDKEY", got)
	}
}
