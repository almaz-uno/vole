package platform

import (
	"runtime"
	"testing"
)

func TestDetect(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Run("auto -> windows on GOOS=windows", func(t *testing.T) {
			if got := Detect(BackendAuto); got != BackendWindows {
				t.Errorf("Detect(auto) = %q, want windows", got)
			}
		})
		t.Run("unknown value -> windows on GOOS=windows", func(t *testing.T) {
			if got := Detect(Backend("nonsense")); got != BackendWindows {
				t.Errorf("Detect(nonsense) = %q, want windows", got)
			}
		})
		t.Run("explicit windows", func(t *testing.T) {
			if got := Detect(BackendWindows); got != BackendWindows {
				t.Errorf("Detect(windows) = %q, want windows", got)
			}
		})
		t.Run("explicit x11 still honored", func(t *testing.T) {
			if got := Detect(BackendX11); got != BackendX11 {
				t.Errorf("Detect(x11) = %q, want x11", got)
			}
		})
		return
	}

	cases := []struct {
		name        string
		cfg         Backend
		wayland     string // WAYLAND_DISPLAY
		sessionType string // XDG_SESSION_TYPE
		want        Backend
	}{
		{"explicit x11 wins over wayland env", BackendX11, "wayland-0", "wayland", BackendX11},
		{"explicit wayland wins over x11 env", BackendWayland, "", "x11", BackendWayland},
		{"explicit windows honored on linux", BackendWindows, "", "x11", BackendWindows},
		{"auto -> wayland via WAYLAND_DISPLAY", BackendAuto, "wayland-0", "", BackendWayland},
		{"auto -> wayland via XDG_SESSION_TYPE", BackendAuto, "", "Wayland", BackendWayland},
		{"auto -> x11 when nothing set", BackendAuto, "", "x11", BackendX11},
		{"unknown value behaves like auto", Backend("nonsense"), "wayland-0", "", BackendWayland},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("WAYLAND_DISPLAY", c.wayland)
			t.Setenv("XDG_SESSION_TYPE", c.sessionType)
			if got := Detect(c.cfg); got != c.want {
				t.Errorf("Detect(%q) with WAYLAND_DISPLAY=%q XDG_SESSION_TYPE=%q = %q, want %q",
					c.cfg, c.wayland, c.sessionType, got, c.want)
			}
		})
	}
}

func TestNopSatisfiesIndicator(t *testing.T) {
	var ind Indicator = Nop{}
	// Exercising the no-ops must not panic.
	ind.Show("ru")
	ind.SetLevel(0.5)
	ind.SetLang("en")
	ind.SetMode(ModeProcessing)
	ind.ShowDownload("large-v3")
	ind.SetProgress(0.5)
	ind.Hide()
	ind.Close()
}
