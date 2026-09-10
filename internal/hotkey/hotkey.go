//go:build linux

// Package hotkey globally intercepts the PTT key via X11 (XGrabKey), without
// depending on i3. On trigger, the passive grab escalates to an active keyboard
// grab until the key is released, so KeyRelease arrives reliably even if the
// modifiers are released first (fixing "stuck" releases); and while the key is
// held, the daemon sees Shift presses and switches the language on the fly.
package hotkey

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/almaz-uno/vole/internal/platform"
	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

var _ platform.Hotkey = (*Grabber)(nil)

// releaseDebounce suppresses spurious releases from the main key's X autorepeat.
const releaseDebounce = 60 * time.Millisecond

const (
	xkShiftL = 0xFFE1
	xkShiftR = 0xFFE2
)

// Config describes the single PTT binding with Shift-based language switching.
type Config struct {
	Mods      string // "Super+Control"
	Key       string // "d"
	LangBase  string // language without Shift, e.g. "ru"
	LangShift string // language while Shift is held, e.g. "en"
}

// Grabber holds the X connection and the grabbed key.
type Grabber struct {
	conn   *xgb.Conn
	root   xproto.Window
	setup  *xproto.SetupInfo
	cfg    Config
	key    xproto.Keycode
	shiftL xproto.Keycode
	shiftR xproto.Keycode
	quit   chan struct{}
}

var ignoreMods = []uint16{
	0,
	uint16(xproto.ModMaskLock),
	uint16(xproto.ModMask2),
	uint16(xproto.ModMaskLock | xproto.ModMask2),
}

// New opens an X connection and grabs the key (with and without Shift).
func New(cfg Config) (*Grabber, error) {
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, err
	}
	setup := xproto.Setup(conn)
	screen := setup.DefaultScreen(conn)
	g := &Grabber{conn: conn, root: screen.Root, setup: setup, cfg: cfg, quit: make(chan struct{})}

	mask, err := parseMods(cfg.Mods)
	if err != nil {
		conn.Close()
		return nil, err
	}
	ks, err := parseKey(cfg.Key)
	if err != nil {
		conn.Close()
		return nil, err
	}
	g.key = g.keysymToKeycode(ks)
	if g.key == 0 {
		conn.Close()
		return nil, fmt.Errorf("hotkey: no keycode for %q", cfg.Key)
	}
	g.shiftL = g.keysymToKeycode(xkShiftL)
	g.shiftR = g.keysymToKeycode(xkShiftR)

	// grab both without and with Shift so start works in either case
	shift := uint16(xproto.ModMaskShift)
	if err := g.grab(mask, g.key); err != nil {
		g.Close()
		return nil, fmt.Errorf("hotkey: grab %s+%s: %w", cfg.Mods, cfg.Key, err)
	}
	if err := g.grab(mask|shift, g.key); err != nil {
		g.Close()
		return nil, fmt.Errorf("hotkey: grab %s+Shift+%s: %w", cfg.Mods, cfg.Key, err)
	}
	return g, nil
}

func (g *Grabber) grab(mask uint16, kc xproto.Keycode) error {
	for _, m := range ignoreMods {
		if err := xproto.GrabKeyChecked(g.conn, true, g.root, mask|m, kc,
			xproto.GrabModeAsync, xproto.GrabModeAsync).Check(); err != nil {
			return err
		}
	}
	return nil
}

// Listen reads events in a blocking loop: onStart on PTT press (with the
// language from the initial Shift), onLang on Shift changes while held and
// onStop on release. Run it in a separate goroutine.
func (g *Grabber) Listen(onStart, onLang, onStop func(lang string)) {
	lang := func(shift bool) string {
		if shift {
			return g.cfg.LangShift
		}
		return g.cfg.LangBase
	}

	var mu sync.Mutex
	recording := false
	shiftDown := false
	var relTimer *time.Timer      // debounce for the main key release
	var shiftOffTimer *time.Timer // deferred language reset on Shift release

	for {
		ev, err := g.conn.WaitForEvent()
		if err != nil || ev == nil {
			select {
			case <-g.quit:
				return
			default:
				continue
			}
		}
		switch e := ev.(type) {
		case xproto.KeyPressEvent:
			switch e.Detail {
			case g.key:
				mu.Lock()
				if relTimer != nil { // cancel the pending release (autorepeat)
					relTimer.Stop()
					relTimer = nil
				}
				if !recording {
					recording = true
					shiftDown = e.State&uint16(xproto.ModMaskShift) != 0
					l := lang(shiftDown)
					mu.Unlock()
					onStart(l)
				} else {
					mu.Unlock()
				}
			case g.shiftL, g.shiftR:
				mu.Lock()
				if shiftOffTimer != nil { // Shift held again — cancel the deferred reset
					shiftOffTimer.Stop()
					shiftOffTimer = nil
				}
				if recording && !shiftDown {
					shiftDown = true
					mu.Unlock()
					onLang(lang(true))
				} else {
					mu.Unlock()
				}
			}
		case xproto.KeyReleaseEvent:
			switch e.Detail {
			case g.key:
				mu.Lock()
				// releasing the main key cancels the deferred Shift reset, so a Shift
				// released together with the key is still taken into account
				if shiftOffTimer != nil {
					shiftOffTimer.Stop()
					shiftOffTimer = nil
				}
				if relTimer != nil {
					relTimer.Stop()
				}
				relTimer = time.AfterFunc(releaseDebounce, func() {
					mu.Lock()
					recording = false
					l := lang(shiftDown)
					shiftDown = false
					relTimer = nil
					mu.Unlock()
					onStop(l)
				})
				mu.Unlock()
			case g.shiftL, g.shiftR:
				mu.Lock()
				// deferred reset: if the key is released next, it gets cancelled (above)
				// and the segment stays on the shift language; otherwise switch back
				if recording && shiftDown {
					if shiftOffTimer != nil {
						shiftOffTimer.Stop()
					}
					shiftOffTimer = time.AfterFunc(releaseDebounce, func() {
						mu.Lock()
						shiftDown = false
						shiftOffTimer = nil
						mu.Unlock()
						onLang(lang(false))
					})
				}
				mu.Unlock()
			}
		}
	}
}

// Close releases the grabs and closes the connection.
func (g *Grabber) Close() {
	select {
	case <-g.quit:
	default:
		close(g.quit)
	}
	xproto.UngrabKey(g.conn, xproto.GrabAny, g.root, xproto.ModMaskAny)
	g.conn.Close()
}

// parseMods parses "Super+Control" into an X11 mask.
func parseMods(s string) (uint16, error) {
	var mask uint16
	for _, part := range strings.Split(s, "+") {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "", "none":
		case "super", "mod4", "win":
			mask |= uint16(xproto.ModMask4)
		case "ctrl", "control":
			mask |= uint16(xproto.ModMaskControl)
		case "alt", "mod1":
			mask |= uint16(xproto.ModMask1)
		case "shift":
			mask |= uint16(xproto.ModMaskShift)
		default:
			return 0, fmt.Errorf("hotkey: unknown modifier %q", part)
		}
	}
	return mask, nil
}

// parseKey returns the keysym for a name (Latin letters/digits and a few special keys).
func parseKey(s string) (uint32, error) {
	s = strings.TrimSpace(s)
	if len(s) == 1 {
		c := s[0]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			return uint32(c), nil
		}
		if c >= 'A' && c <= 'Z' {
			return uint32(c - 'A' + 'a'), nil
		}
	}
	switch strings.ToLower(s) {
	case "space":
		return 0x0020, nil
	case "pause":
		return 0xFF13, nil
	case "menu":
		return 0xFF67, nil
	}
	return 0, fmt.Errorf("hotkey: unknown key %q", s)
}

// keysymToKeycode maps a keysym to a keycode for the current layout.
func (g *Grabber) keysymToKeycode(keysym uint32) xproto.Keycode {
	mapping, err := xproto.GetKeyboardMapping(g.conn,
		g.setup.MinKeycode, byte(g.setup.MaxKeycode-g.setup.MinKeycode+1)).Reply()
	if err != nil {
		return 0
	}
	for kc := g.setup.MinKeycode; kc <= g.setup.MaxKeycode; kc++ {
		for i := byte(0); i < mapping.KeysymsPerKeycode; i++ {
			idx := int(kc-g.setup.MinKeycode)*int(mapping.KeysymsPerKeycode) + int(i)
			if idx < len(mapping.Keysyms) && uint32(mapping.Keysyms[idx]) == keysym {
				return kc
			}
		}
	}
	return 0
}
