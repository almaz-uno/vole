//go:build windows

// Package winhotkey implements platform.Hotkey via RegisterHotKey.
// Two shortcuts are registered: mods+key (base language) and mods+Shift+key
// (alt language). RegisterHotKey reports press only; release is observed with
// GetAsyncKeyState on the main key — the same press/release model as Wayland,
// without X11-style live-Shift.
package winhotkey

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/almaz-uno/vole/internal/platform"
	"golang.org/x/sys/windows"
)

const (
	wmHotkey    = 0x0312
	wmQuit      = 0x0012
	modAlt      = 0x0001
	modControl  = 0x0002
	modShift    = 0x0004
	modWin      = 0x0008
	modNorepeat = 0x4000

	idBase = 1
	idAlt  = 2
)

var _ platform.Hotkey = (*Grabber)(nil)

// Config describes the PTT binding. The alt language is a separate hotkey
// (mods+Shift+key), not a live-Shift toggle.
type Config struct {
	Mods      string
	Key       string
	LangBase  string
	LangShift string
	OnError   func(error) // optional: RegisterHotKey failure (busy combo)
}

// Grabber owns the RegisterHotKey registrations and the message loop thread.
type Grabber struct {
	cfg Config
	tid uint32
	mu  sync.Mutex
}

var (
	modUser32             = windows.NewLazySystemDLL("user32.dll")
	procRegisterHotKey    = modUser32.NewProc("RegisterHotKey")
	procUnregisterHotKey  = modUser32.NewProc("UnregisterHotKey")
	procGetMessage        = modUser32.NewProc("GetMessageW")
	procPostThreadMessage = modUser32.NewProc("PostThreadMessageW")
	procGetAsyncKeyState  = modUser32.NewProc("GetAsyncKeyState")
)

type msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
	_       uint32 // padding on 64-bit
}

// New constructs a Grabber. Registration happens in Listen (same thread as
// the message loop). Errors from a busy hotkey are reported from Listen.
func New(cfg Config) (*Grabber, error) {
	if _, err := parseMods(cfg.Mods); err != nil {
		return nil, err
	}
	if _, err := parseVK(cfg.Key); err != nil {
		return nil, err
	}
	return &Grabber{cfg: cfg}, nil
}

func (g *Grabber) Listen(onStart, onLang, onStop func(lang string)) {
	_ = onLang // Windows has no live-Shift
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	g.mu.Lock()
	g.tid = windows.GetCurrentThreadId()
	g.mu.Unlock()

	mods, err := parseMods(g.cfg.Mods)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vole daemon: hotkey:", err)
		return
	}
	vk, err := parseVK(g.cfg.Key)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vole daemon: hotkey:", err)
		return
	}

	baseOK := registerHotKey(idBase, mods|modNorepeat, vk)
	altOK := registerHotKey(idAlt, mods|modShift|modNorepeat, vk)
	if !baseOK && !altOK {
		err := fmt.Errorf("RegisterHotKey failed for %s+%s (already in use?)", g.cfg.Mods, g.cfg.Key)
		fmt.Fprintln(os.Stderr, "vole daemon: hotkey:", err)
		if g.cfg.OnError != nil {
			g.cfg.OnError(err)
		}
		return
	}
	if !baseOK {
		err := fmt.Errorf("%s+%s is already in use — base PTT disabled", g.cfg.Mods, g.cfg.Key)
		fmt.Fprintln(os.Stderr, "vole daemon: hotkey:", err)
		if g.cfg.OnError != nil {
			g.cfg.OnError(err)
		}
	}
	if !altOK {
		err := fmt.Errorf("%s+Shift+%s is already in use — alt PTT disabled", g.cfg.Mods, g.cfg.Key)
		fmt.Fprintln(os.Stderr, "vole daemon: hotkey:", err)
		if g.cfg.OnError != nil {
			g.cfg.OnError(err)
		}
	}
	defer func() {
		unregisterHotKey(idBase)
		unregisterHotKey(idAlt)
	}()

	var m msg
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		ret := int32(r)
		if ret <= 0 {
			return // WM_QUIT or error
		}
		if m.message != wmHotkey {
			continue
		}
		var lang string
		switch m.wParam {
		case idBase:
			lang = g.cfg.LangBase
		case idAlt:
			lang = g.cfg.LangShift
		default:
			continue
		}
		onStart(lang)
		waitKeyRelease(vk)
		onStop(lang)
	}
}

func (g *Grabber) Close() {
	g.mu.Lock()
	tid := g.tid
	g.mu.Unlock()
	if tid != 0 {
		procPostThreadMessage.Call(uintptr(tid), wmQuit, 0, 0)
	}
}

func registerHotKey(id int, mods, vk uint32) bool {
	r, _, _ := procRegisterHotKey.Call(0, uintptr(id), uintptr(mods), uintptr(vk))
	return r != 0
}

func unregisterHotKey(id int) {
	procUnregisterHotKey.Call(0, uintptr(id))
}

func waitKeyRelease(vk uint32) {
	// RegisterHotKey fires on press. Poll until the main key is up so PTT
	// release matches Wayland/X11. 10 ms is plenty for dictation.
	for {
		st, _, _ := procGetAsyncKeyState.Call(uintptr(vk))
		if int16(st) >= 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func parseMods(s string) (uint32, error) {
	var mask uint32
	for _, part := range strings.Split(s, "+") {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "", "none":
		case "ctrl", "control":
			mask |= modControl
		case "alt", "mod1":
			mask |= modAlt
		case "shift":
			mask |= modShift
		case "super", "mod4", "win":
			mask |= modWin
		default:
			return 0, fmt.Errorf("hotkey: unknown modifier %q", part)
		}
	}
	return mask, nil
}

func parseVK(s string) (uint32, error) {
	s = strings.TrimSpace(s)
	if len(s) == 1 {
		c := s[0]
		if c >= 'a' && c <= 'z' {
			return uint32(c - 'a' + 'A'), nil // VK_A..VK_Z
		}
		if c >= 'A' && c <= 'Z' {
			return uint32(c), nil
		}
		if c >= '0' && c <= '9' {
			return uint32(c), nil
		}
	}
	switch strings.ToLower(s) {
	case "space":
		return 0x20, nil
	case "pause":
		return 0x13, nil
	}
	return 0, fmt.Errorf("hotkey: unknown key %q", s)
}
