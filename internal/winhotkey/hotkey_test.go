//go:build windows

package winhotkey

import "testing"

func TestParseMods(t *testing.T) {
	got, err := parseMods("Control+Alt")
	if err != nil {
		t.Fatal(err)
	}
	if got != modControl|modAlt {
		t.Errorf("Control+Alt = %#x, want %#x", got, modControl|modAlt)
	}
	got, err = parseMods("ctrl+alt+shift")
	if err != nil {
		t.Fatal(err)
	}
	if got != modControl|modAlt|modShift {
		t.Errorf("ctrl+alt+shift = %#x", got)
	}
	if _, err := parseMods("Hyper"); err == nil {
		t.Error("expected error for unknown modifier")
	}
}

func TestParseModsRejectsNoModifiers(t *testing.T) {
	for _, spec := range []string{"", "none"} {
		if _, err := parseMods(spec); err == nil {
			t.Errorf("parseMods(%q) should reject a modifier-free global hotkey", spec)
		}
	}
}

func TestParseVK(t *testing.T) {
	vk, err := parseVK("d")
	if err != nil {
		t.Fatal(err)
	}
	if vk != 'D' {
		t.Errorf("d = %d, want VK_D (%d)", vk, 'D')
	}
	if _, err := parseVK("f13"); err == nil {
		t.Error("expected error for unknown key")
	}
}

func TestConfiguredModifiersDownUsesConfiguredWinKey(t *testing.T) {
	down := map[uint32]bool{
		0x5B: true, // VK_LWIN
	}
	keyDown := func(vk uint32) bool { return down[vk] }

	if !configuredModifiersDown(modWin, keyDown) {
		t.Fatal("configured Win modifier should be reported as held")
	}

	down = map[uint32]bool{
		0x11: true, // VK_CONTROL
		0x12: true, // VK_MENU
	}
	if configuredModifiersDown(modWin, keyDown) {
		t.Fatal("unconfigured Ctrl+Alt must not satisfy a Win binding")
	}
}
