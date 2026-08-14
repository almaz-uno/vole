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
