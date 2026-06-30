// Package tray renders a system-tray icon via StatusNotifier (DBus/SNI).
// The circle color reflects the daemon state; the menu toggles dictation on/off
// and lists recent dictations (click one to paste it).
package tray

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
	"sync"

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
	colDisabled = color.NRGBA{90, 90, 95, 160} // dimmed — dictation off
	colIdle     = color.NRGBA{120, 130, 150, 255}
	colRec      = color.NRGBA{229, 57, 53, 255} // red — recording
	colProc     = color.NRGBA{255, 179, 0, 255} // amber — transcribing
)

// Tray manages the tray icon and its menu.
type Tray struct {
	disabled, idle, rec, proc []byte
	mToggle                   *systray.MenuItem
	mAutoPaste                *systray.MenuItem // "Auto-paste" checkbox; nil if not applicable

	onHistory func(idx int) // invoked with the index of a clicked history entry

	mu          sync.Mutex
	hist        []*systray.MenuItem // history pool (newest first); nil until ready
	pendingHist []string            // history set before the menu was built
}

// Run starts the tray icon in a goroutine. onToggle toggles dictation; onQuit
// runs on "Quit"; onTap runs on a left-click of the icon (start/stop recording);
// onHistory(idx) runs when the user clicks recent-dictation idx. historySize is
// the number of history slots in the menu. onAutoPaste, if non-nil, adds an
// "Auto-paste" checkbox (initial state autoPasteOn) that runs it on each click;
// pass nil to omit the item (e.g. the non-paste injector). The returned Tray can
// be updated immediately, before the tray host is ready.
func Run(onToggle, onQuit, onTap func(), onHistory func(idx int), historySize int, onAutoPaste func(), autoPasteOn bool) *Tray {
	t := &Tray{
		disabled:  pngCircle(colDisabled),
		idle:      pngCircle(colIdle),
		rec:       pngCircle(colRec),
		proc:      pngCircle(colProc),
		onHistory: onHistory,
	}
	if historySize < 0 {
		historySize = 0
	}
	// Wire the left-click handler before the item registers; this also makes the
	// menu open on right-click (ItemIsMenu becomes false) consistently across hosts.
	if onTap != nil {
		systray.SetOnTapped(onTap)
	}
	ready := func() {
		systray.SetTitle("vole")
		systray.SetTooltip("vole — voice dictation")
		systray.SetIcon(t.idle)

		// controls first, set off by a separator, so they stay reachable above a
		// long history list
		t.mToggle = systray.AddMenuItem("Disable dictation", "Pause/resume push-to-talk")
		var autoPasteCh <-chan struct{}
		if onAutoPaste != nil {
			t.mAutoPaste = systray.AddMenuItemCheckbox("Auto-paste",
				"Paste after dictation (off: only copy to the clipboard)", autoPasteOn)
			autoPasteCh = t.mAutoPaste.ClickedCh
		}
		mQuit := systray.AddMenuItem("Quit", "Stop vole")
		go func() {
			for {
				select {
				case <-t.mToggle.ClickedCh:
					onToggle()
				case <-autoPasteCh: // nil when omitted: this case never fires
					if onAutoPaste != nil {
						onAutoPaste()
					}
				case <-mQuit.ClickedCh:
					systray.Quit()
					return
				}
			}
		}()
		systray.AddSeparator()

		// recent dictations (header + a hidden pool, newest first)
		header := systray.AddMenuItem("Recent dictations", "")
		header.Disable()
		hist := make([]*systray.MenuItem, historySize)
		for i := range hist {
			it := systray.AddMenuItem("", "")
			it.Hide()
			hist[i] = it
			go func(idx int, ch <-chan struct{}) {
				for range ch {
					if t.onHistory != nil {
						t.onHistory(idx)
					}
				}
			}(i, it.ClickedCh)
		}

		t.mu.Lock()
		t.hist = hist
		pending := t.pendingHist
		t.pendingHist = nil
		t.mu.Unlock()
		if pending != nil {
			t.SetHistory(pending)
		}
	}
	go systray.Run(ready, onQuit)
	return t
}

// SetHistory updates the recent-dictations menu (texts newest first). The label
// is a one-line truncation; the full text is the tooltip. Safe to call before
// the menu is built (applied once it is).
func (t *Tray) SetHistory(texts []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.hist == nil {
		t.pendingHist = texts
		return
	}
	for i, it := range t.hist {
		if i < len(texts) {
			it.SetTitle(histLabel(texts[i]))
			it.SetTooltip(texts[i])
			it.Show()
		} else {
			it.Hide()
		}
	}
}

// histLabel collapses whitespace and truncates text to a one-line menu label.
func histLabel(text string) string {
	s := strings.Join(strings.Fields(text), " ")
	const max = 50
	if r := []rune(s); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
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

// SetAutoPaste reflects the auto-paste toggle state in the menu checkbox.
func (t *Tray) SetAutoPaste(on bool) {
	if t.mAutoPaste == nil {
		return
	}
	if on {
		t.mAutoPaste.Check()
	} else {
		t.mAutoPaste.Uncheck()
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
