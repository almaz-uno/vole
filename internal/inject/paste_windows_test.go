//go:build windows

package inject

import "testing"

func TestParsePasteKey(t *testing.T) {
	cases := []struct {
		in   string
		want []uint16
	}{
		{"ctrl+v", []uint16{vkControl, 0x56}},
		{"Control+V", []uint16{vkControl, 0x56}},
		{"shift+insert", []uint16{vkShift, vkInsert}},
		{"ctrl+shift+v", []uint16{vkControl, vkShift, 0x56}},
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
