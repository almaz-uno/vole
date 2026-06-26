package wayland

import "testing"

func TestPortalTrigger(t *testing.T) {
	cases := []struct {
		name      string
		mods, key string
		withShift bool
		want      string
	}{
		{"super+k", "Super", "k", false, "LOGO+k"},
		{"super+k alt adds shift", "Super", "k", true, "LOGO+SHIFT+k"},
		{"ctrl+super combo", "Super+Control", "d", false, "LOGO+CTRL+d"},
		{"uppercase key lowercased", "Super", "K", false, "LOGO+k"},
		{"no duplicate shift", "Super+Shift", "k", true, "LOGO+SHIFT+k"},
		{"no mods, bare key", "", "k", false, "k"},
		{"unknown key -> empty", "Super", "f1", false, ""},
		{"named space key", "Super", "space", false, "LOGO+space"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := portalTrigger(c.mods, c.key, c.withShift); got != c.want {
				t.Errorf("portalTrigger(%q, %q, %v) = %q, want %q", c.mods, c.key, c.withShift, got, c.want)
			}
		})
	}
}
