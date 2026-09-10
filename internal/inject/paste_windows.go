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

const defaultPasteKey = "unicode"

const (
	inputKeyboard     = 1
	keyeventfExtended = 0x0001
	keyeventfKeyup    = 0x0002
	keyeventfUnicode  = 0x0004
	keyeventfScancode = 0x0008
	vkControl         = 0x11
	vkLControl        = 0xA2
	vkRControl        = 0xA3
	vkShift           = 0x10
	vkLShift          = 0xA0
	vkRShift          = 0xA1
	vkMenu            = 0x12
	vkLMenu           = 0xA4
	vkRMenu           = 0xA5
	vkLWin            = 0x5B
	vkRWin            = 0x5C
	vkInsert          = 0x2D
	cfUnicodeText     = 13
	inputSize         = 40 // sizeof(INPUT) on windows/amd64
	ownDeadline       = 500 * time.Millisecond
	scanLControl      = 0x1D
	scanLShift        = 0x2A
	scanRShift        = 0x36
	scanLMenu         = 0x38
	scanInsert        = 0x52
	scanV             = 0x2F
)

// Paster puts text on the Win32 clipboard (CF_UNICODETEXT) and injects it with
// Unicode SendInput or a configured paste key combination.
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
	modUser32                    = windows.NewLazySystemDLL("user32.dll")
	modKernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	procSendInput                = modUser32.NewProc("SendInput")
	procOpenClipboard            = modUser32.NewProc("OpenClipboard")
	procCloseClipboard           = modUser32.NewProc("CloseClipboard")
	procEmptyClipboard           = modUser32.NewProc("EmptyClipboard")
	procSetClipboardData         = modUser32.NewProc("SetClipboardData")
	procGetClipboardData         = modUser32.NewProc("GetClipboardData")
	procMapVirtualKey            = modUser32.NewProc("MapVirtualKeyW")
	procGetForegroundWindow      = modUser32.NewProc("GetForegroundWindow")
	procGetWindowThreadProcessId = modUser32.NewProc("GetWindowThreadProcessId")
	procAttachThreadInput        = modUser32.NewProc("AttachThreadInput")
	procGetCurrentThreadId       = modKernel32.NewProc("GetCurrentThreadId")
	procGlobalAlloc              = modKernel32.NewProc("GlobalAlloc")
	procGlobalLock               = modKernel32.NewProc("GlobalLock")
	procGlobalUnlock             = modKernel32.NewProc("GlobalUnlock")
	procGlobalFree               = modKernel32.NewProc("GlobalFree")
	procRtlMoveMemory            = modKernel32.NewProc("RtlMoveMemory")
)

const gmemMoveable = 0x0002

// NewPaste returns a Win32 clipboard+SendInput paste injector.
func NewPaste(pasteKey string, autoPaste bool) *Paster {
	if pasteKey == "" {
		pasteKey = defaultPasteKey
	}
	p := &Paster{pasteKey: pasteKey}
	if !useUnicodeInput(pasteKey) {
		p.keys = parsePasteKey(pasteKey)
	}
	p.autoPaste.Store(autoPaste)
	fmt.Fprintf(os.Stderr, "[vole] inject(paste): Win32 clipboard + SendInput %q\n", pasteKey)
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

// onOSThread runs fn pinned to a single OS thread. Clipboard ownership and
// AttachThreadInput are per-thread state: if the goroutine were rescheduled
// between OpenClipboard and CloseClipboard the clipboard would stay open — and
// locked for every other process — and an attach/detach pair split across two
// threads would attach input to a thread that never calls SendInput.
func onOSThread(fn func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	return fn()
}

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
	time.Sleep(20 * time.Millisecond)
	if useUnicodeInput(p.pasteKey) {
		if err := sendUnicode(text); err != nil {
			return fmt.Errorf("inject(paste): Unicode SendInput: %w", err)
		}
	} else if err := sendKeyCombo(p.keys); err != nil {
		return fmt.Errorf("inject(paste): SendInput %s: %w", p.pasteKey, err)
	}
	return nil
}

func useUnicodeInput(spec string) bool {
	return strings.EqualFold(strings.TrimSpace(spec), "unicode")
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
	return onOSThread(func() error { return setClipboardLocked(text) })
}

func setClipboardLocked(text string) error {
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
	var out string
	err := onOSThread(func() error {
		s, err := getClipboardLocked()
		out = s
		return err
	})
	return out, err
}

func getClipboardLocked() (string, error) {
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
			keys = append(keys, vkLControl)
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
				return []uint16{vkLControl, 0x56} // fallback Ctrl+V
			}
		}
	}
	if len(keys) == 0 {
		return []uint16{vkLControl, 0x56}
	}
	return keys
}

func releaseModifiers() {
	for _, vk := range []uint16{vkLControl, vkRControl, vkLShift, vkRShift, vkLMenu, vkRMenu, vkLWin, vkRWin} {
		_ = sendKey(vk, keyeventfKeyup)
	}
}

func sendUnicode(text string) error {
	return withForegroundInput(func() error {
		return sendUnicodeUnlocked(text)
	})
}

func sendKeyCombo(keys []uint16) error {
	return withForegroundInput(func() error {
		return sendKeyComboUnlocked(keys)
	})
}

func sendUnicodeUnlocked(text string) error {
	units, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}
	if len(units) > 0 && units[len(units)-1] == 0 {
		units = units[:len(units)-1]
	}
	if len(units) == 0 {
		return nil
	}
	const chunk = 32
	for i := 0; i < len(units); i += chunk {
		j := i + chunk
		if j > len(units) {
			j = len(units)
		}
		part := units[i:j]
		n := len(part) * 2
		buf := make([]byte, inputSize*n)
		for k, u := range part {
			writeUnicodeInput(buf, (k*2)*inputSize, u, keyeventfUnicode)
			writeUnicodeInput(buf, (k*2+1)*inputSize, u, keyeventfUnicode|keyeventfKeyup)
		}
		sent, _, err := procSendInput.Call(uintptr(n), uintptr(unsafe.Pointer(&buf[0])), inputSize)
		if int(sent) != n {
			return fmt.Errorf("SendInput: %w", err)
		}
	}
	return nil
}

func writeUnicodeInput(buf []byte, off int, unit uint16, flags uint32) {
	binary.LittleEndian.PutUint32(buf[off:], inputKeyboard)
	binary.LittleEndian.PutUint16(buf[off+8:], 0) // wVk must be 0
	binary.LittleEndian.PutUint16(buf[off+10:], unit)
	binary.LittleEndian.PutUint32(buf[off+12:], flags)
}

func withForegroundInput(fn func() error) error {
	return onOSThread(func() error { return withForegroundInputLocked(fn) })
}

func withForegroundInputLocked(fn func() error) error {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return fn()
	}
	tid, _, _ := procGetWindowThreadProcessId.Call(hwnd, 0)
	our, _, _ := procGetCurrentThreadId.Call()
	if tid == 0 || tid == our {
		return fn()
	}
	r, _, _ := procAttachThreadInput.Call(our, tid, 1)
	if r != 0 {
		defer procAttachThreadInput.Call(our, tid, 0)
	}
	return fn()
}

func vkScan(vk uint16) uint16 {
	switch vk {
	case vkControl, vkLControl, vkRControl:
		return scanLControl
	case vkShift, vkLShift:
		return scanLShift
	case vkRShift:
		return scanRShift
	case vkMenu, vkLMenu, vkRMenu:
		return scanLMenu
	case vkInsert:
		return scanInsert
	case 0x56: // V
		return scanV
	default:
		r, _, _ := procMapVirtualKey.Call(uintptr(vk), 0)
		return uint16(r)
	}
}

func keyFlags(vk uint16, keyup bool) uint32 {
	f := uint32(keyeventfScancode)
	if keyup {
		f |= keyeventfKeyup
	}
	switch vk {
	case vkRControl, vkRMenu, vkInsert, vkLWin, vkRWin:
		f |= keyeventfExtended
	}
	return f
}

func writeKeyInput(buf []byte, off int, vk uint16, flags uint32) {
	binary.LittleEndian.PutUint32(buf[off:], inputKeyboard)
	binary.LittleEndian.PutUint16(buf[off+8:], vk)
	binary.LittleEndian.PutUint16(buf[off+10:], vkScan(vk))
	binary.LittleEndian.PutUint32(buf[off+12:], flags)
}

func sendKeyComboUnlocked(keys []uint16) error {
	n := len(keys) * 2
	buf := make([]byte, inputSize*n)
	for i, k := range keys {
		writeKeyInput(buf, i*inputSize, k, keyFlags(k, false))
	}
	for i := len(keys) - 1; i >= 0; i-- {
		off := (len(keys) + (len(keys) - 1 - i)) * inputSize
		writeKeyInput(buf, off, keys[i], keyFlags(keys[i], true))
	}
	sent, _, err := procSendInput.Call(uintptr(n), uintptr(unsafe.Pointer(&buf[0])), inputSize)
	if int(sent) != n {
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
		f := flags[i] | keyeventfScancode
		if keyFlags(keys[i], false)&keyeventfExtended != 0 {
			f |= keyeventfExtended
		}
		writeKeyInput(buf, i*inputSize, keys[i], f)
	}
	n, _, err := procSendInput.Call(uintptr(len(keys)), uintptr(unsafe.Pointer(&buf[0])), inputSize)
	if int(n) != len(keys) {
		return err
	}
	return nil
}
