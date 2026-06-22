// Package tray renders a system-tray icon via StatusNotifier (DBus/SNI).
// The circle color reflects the daemon state, and a menu item toggles
// dictation on/off (push-to-talk is ignored while disabled).
package tray

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"

	"fyne.io/systray"
)

// State is the visual state shown by the icon.
type State int

const (
	StateDisabled State = iota
	StateIdle
	StateRecording
	StateProcessing
)

var (
	colDisabled = color.NRGBA{90, 90, 95, 160}  // dimmed — dictation off
	colIdle     = color.NRGBA{120, 130, 150, 255}
	colRec      = color.NRGBA{229, 57, 53, 255}  // red — recording
	colProc     = color.NRGBA{255, 179, 0, 255}  // amber — transcribing
)

// Tray manages the tray icon and its menu.
type Tray struct {
	disabled, idle, rec, proc []byte
	mToggle                   *systray.MenuItem
}

// Run starts the tray icon in a goroutine. onToggle is called when the user
// clicks the enable/disable item; onQuit — on "Quit". The returned Tray can be
// updated immediately, before the tray host is ready.
func Run(onToggle, onQuit func()) *Tray {
	t := &Tray{
		disabled: pngCircle(colDisabled),
		idle:     pngCircle(colIdle),
		rec:      pngCircle(colRec),
		proc:     pngCircle(colProc),
	}
	ready := func() {
		systray.SetTitle("vole")
		systray.SetTooltip("vole — voice dictation")
		systray.SetIcon(t.idle)
		t.mToggle = systray.AddMenuItem("Disable dictation", "Pause/resume push-to-talk")
		mQuit := systray.AddMenuItem("Quit", "Stop vole")
		go func() {
			for {
				select {
				case <-t.mToggle.ClickedCh:
					onToggle()
				case <-mQuit.ClickedCh:
					systray.Quit()
					return
				}
			}
		}()
	}
	go systray.Run(ready, onQuit)
	return t
}

// SetEnabled reflects the enabled/disabled state in the icon and menu label.
func (t *Tray) SetEnabled(enabled bool) {
	if t.mToggle != nil {
		if enabled {
			t.mToggle.SetTitle("Disable dictation")
		} else {
			t.mToggle.SetTitle("Enable dictation")
		}
	}
	if enabled {
		systray.SetIcon(t.idle)
	} else {
		systray.SetIcon(t.disabled)
	}
}

// SetTooltip updates the tray tooltip (e.g. download progress).
func (t *Tray) SetTooltip(s string) {
	systray.SetTooltip(s)
}

// SetState updates the icon for the current dictation phase.
func (t *Tray) SetState(s State) {
	switch s {
	case StateDisabled:
		systray.SetIcon(t.disabled)
	case StateRecording:
		systray.SetIcon(t.rec)
	case StateProcessing:
		systray.SetIcon(t.proc)
	default:
		systray.SetIcon(t.idle)
	}
}

// pngCircle draws a filled antialiased circle of the given color (PNG, alpha).
func pngCircle(c color.NRGBA) []byte {
	const s = 64
	img := image.NewNRGBA(image.Rect(0, 0, s, s))
	cx, cy, r := float64(s)/2, float64(s)/2, float64(s)*0.40
	for y := 0; y < s; y++ {
		for x := 0; x < s; x++ {
			d := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
			cov := r - d + 0.7
			if cov <= 0 {
				continue
			}
			if cov > 1 {
				cov = 1
			}
			img.SetNRGBA(x, y, color.NRGBA{c.R, c.G, c.B, uint8(float64(c.A) * cov)})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
