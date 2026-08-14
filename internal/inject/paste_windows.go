//go:build windows

package inject

import (
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/almaz-uno/vole/internal/platform"
	"golang.org/x/sys/windows"
)

const defaultPasteKey = "ctrl+v"

const (
	inputKeyboard  = 1
	keyeventfKeyup = 0x0002
	vkControl      = 0x11
	vkLControl     = 0xA2
	vkRControl     = 0xA3
	vkShift        = 0x10
	vkLShift       = 0xA0
	vkRShift       = 0xA1
	vkMenu         = 0x12
	vkLMenu        = 0xA4
	vkRMenu        = 0xA5
	vkLWin         = 0x5B
	vkRWin         = 0x5C
	vkInsert       = 0x2D
	cfUnicodeText  = 13
	inputSize      = 40 // sizeof(INPUT) on windows/amd64
	ownDeadline    = 500 * time.Millisecond
)

// Paster injects text by putting it on the Win32 clipboard (CF_UNICODETEXT)
// and synthesizing a paste keystroke with SendInput. Default combo is Ctrl+V.
type Paster struct {
	keys      []uint16 // virtual-key codes in press order
	pasteKey  string
	autoPaste atomic.Bool
}

var (
	_ platform.Injector   = (*Paster)(nil)
	_ platform.AutoPaster = (*Paster)(nil)
	_ platform.Inserter   = (*Paster)(nil)
	_ platform.Copier     = (*Paster)(nil)
)

var (
	modUser32            = windows.NewLazySystemDLL("user32.dll")
	modKernel32          = windows.NewLazySystemDLL("kernel32.dll")
	procSendInput        = modUser32.NewProc("SendInput")
	procOpenClipboard    = modUser32.NewProc("OpenClipboard")
	procCloseClipboard   = modUser32.NewProc("CloseClipboard")
	procEmptyClipboard   = modUser32.NewProc("EmptyClipboard")
	procSetClipboardData = modUser32.NewProc("SetClipboardData")
	procGetClipboardData = modUser32.NewProc("GetClipboardData")
	procGlobalAlloc      = modKernel32.NewProc("GlobalAlloc")
	procGlobalLock       = modKernel32.NewProc("GlobalLock")
	procGlobalUnlock     = modKernel32.NewProc("GlobalUnlock")
	procGlobalFree       = modKernel32.NewProc("GlobalFree")
	procRtlMoveMemory    = modKernel32.NewProc("RtlMoveMemory")
)

const gmemMoveable = 0x0002

// NewPaste returns a Win32 clipboard+SendInput paste injector.
func NewPaste(pasteKey string, autoPaste bool) *Paster {
	if pasteKey == "" {
		pasteKey = defaultPasteKey
	}
	p := &Paster{pasteKey: pasteKey, keys: parsePasteKey(pasteKey)}
	p.autoPaste.Store(autoPaste)
	fmt.Fprintf(os.Stderr, "[vole] inject(paste): Win32 clipboard, paste via %q\n", pasteKey)
	return p
}

func (p *Paster) Type(text string) error   { return p.put(text, p.autoPaste.Load()) }
func (p *Paster) AutoPaste() bool          { return p.autoPaste.Load() }
func (p *Paster) SetAutoPaste(v bool)      { p.autoPaste.Store(v) }
func (p *Paster) Insert(text string) error { return p.put(text, true) }
func (p *Paster) Copy(text string) error   { return p.put(text, false) }
func (p *Paster) CopyHistory(string) error { return nil } // no clipboard-history backend
func (p *Paster) Available() bool          { return true }
func (p *Paster) Close()                   {}

func (p *Paster) put(text string, paste bool) error {
	if text == "" {
		return nil
	}
	if err := setClipboard(text); err != nil {
		return fmt.Errorf("inject(paste): clipboard: %w", err)
	}
	p.waitOwned(text)
	if !paste {
		return nil
	}
	releaseModifiers()
	if err := sendKeyCombo(p.keys); err != nil {
		return fmt.Errorf("inject(paste): SendInput %s: %w", p.pasteKey, err)
	}
	return nil
}

func (p *Paster) waitOwned(text string) {
	deadline := time.Now().Add(ownDeadline)
	for {
		got, err := getClipboard()
		if err == nil && got == text {
			return
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func setClipboard(text string) error {
	utf16, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}
	size := len(utf16) * 2
	h, _, err := procGlobalAlloc.Call(gmemMoveable, uintptr(size))
	if h == 0 {
		return fmt.Errorf("GlobalAlloc: %w", err)
	}
	ptr, _, err := procGlobalLock.Call(h)
	if ptr == 0 {
		procGlobalFree.Call(h)
		return fmt.Errorf("GlobalLock: %w", err)
	}
	procRtlMoveMemory.Call(ptr, uintptr(unsafe.Pointer(&utf16[0])), uintptr(size))
	runtime.KeepAlive(utf16)
	procGlobalUnlock.Call(h)

	if err := openClipboardRetry(); err != nil {
		procGlobalFree.Call(h)
		return err
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	r, _, err := procSetClipboardData.Call(cfUnicodeText, h)
	if r == 0 {
		procGlobalFree.Call(h)
		return fmt.Errorf("SetClipboardData: %w", err)
	}
	return nil
}

func getClipboard() (string, error) {
	if err := openClipboardRetry(); err != nil {
		return "", err
	}
	defer procCloseClipboard.Call()
	h, _, err := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", err
	}
	ptr, _, err := procGlobalLock.Call(h)
	if ptr == 0 {
		return "", fmt.Errorf("GlobalLock: %w", err)
	}
	s := utf16CString(ptr)
	procGlobalUnlock.Call(h)
	return s, nil
}

func utf16CString(ptr uintptr) string {
	n := 0
	var w uint16
	for {
		procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&w)), ptr+uintptr(n)*2, 2)
		if w == 0 {
			break
		}
		n++
		if n > 1<<20 {
			break
		}
	}
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n)
	procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&buf[0])), ptr, uintptr(n*2))
	runtime.KeepAlive(buf)
	return windows.UTF16ToString(buf)
}

func openClipboardRetry() error {
	var err error
	for i := 0; i < 20; i++ {
		r, _, e := procOpenClipboard.Call(0)
		if r != 0 {
			return nil
		}
		err = e
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("OpenClipboard: %w", err)
}

func parsePasteKey(spec string) []uint16 {
	parts := strings.Split(strings.ToLower(spec), "+")
	keys := make([]uint16, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		switch p {
		case "ctrl", "control", "ctrl_l", "control_l":
			keys = append(keys, vkControl)
		case "shift", "shift_l":
			keys = append(keys, vkShift)
		case "alt", "alt_l", "menu":
			keys = append(keys, vkMenu)
		case "win", "super", "meta":
			keys = append(keys, vkLWin)
		case "insert":
			keys = append(keys, vkInsert)
		default:
			if len(p) == 1 && p[0] >= 'a' && p[0] <= 'z' {
				keys = append(keys, uint16(p[0]-'a'+0x41)) // VK_A..VK_Z
			} else {
				return []uint16{vkControl, 0x56} // fallback Ctrl+V
			}
		}
	}
	if len(keys) == 0 {
		return []uint16{vkControl, 0x56}
	}
	return keys
}

func releaseModifiers() {
	for _, vk := range []uint16{vkLControl, vkRControl, vkLShift, vkRShift, vkLMenu, vkRMenu, vkLWin, vkRWin} {
		_ = sendKey(vk, keyeventfKeyup)
	}
}

func sendKeyCombo(keys []uint16) error {
	// press in order, release in reverse
	seq := make([]uint16, 0, len(keys)*2)
	flags := make([]uint32, 0, len(keys)*2)
	for _, k := range keys {
		seq = append(seq, k)
		flags = append(flags, 0)
	}
	for i := len(keys) - 1; i >= 0; i-- {
		seq = append(seq, keys[i])
		flags = append(flags, keyeventfKeyup)
	}
	buf := make([]byte, inputSize*len(seq))
	for i := range seq {
		off := i * inputSize
		binary.LittleEndian.PutUint32(buf[off:], inputKeyboard)
		binary.LittleEndian.PutUint16(buf[off+8:], seq[i])
		binary.LittleEndian.PutUint32(buf[off+12:], flags[i])
	}
	n, _, err := procSendInput.Call(uintptr(len(seq)), uintptr(unsafe.Pointer(&buf[0])), inputSize)
	if int(n) != len(seq) {
		return fmt.Errorf("SendInput: %w", err)
	}
	return nil
}

func sendKey(vk uint16, flags uint32) error {
	return sendKeyComboFlags([]uint16{vk}, []uint32{flags})
}

func sendKeyComboFlags(keys []uint16, flags []uint32) error {
	buf := make([]byte, inputSize*len(keys))
	for i := range keys {
		off := i * inputSize
		binary.LittleEndian.PutUint32(buf[off:], inputKeyboard)
		binary.LittleEndian.PutUint16(buf[off+8:], keys[i])
		binary.LittleEndian.PutUint32(buf[off+12:], flags[i])
	}
	n, _, err := procSendInput.Call(uintptr(len(keys)), uintptr(unsafe.Pointer(&buf[0])), inputSize)
	if int(n) != len(keys) {
		return err
	}
	return nil
}
