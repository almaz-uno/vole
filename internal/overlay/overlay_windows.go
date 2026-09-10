//go:build windows

// Package overlay is a floating indicator at the top of the screen: a
// click-through, no-activate layered window so Windows tray overflow does not
// hide recording (red) / transcribing (amber) status.
package overlay

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/almaz-uno/vole/internal/platform"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/sys/windows"
)

var _ platform.Indicator = (*Overlay)(nil)

const (
	wsPopup               = 0x80000000
	wsExLayered           = 0x00080000
	wsExTransparent       = 0x00000020
	wsExToolwindow        = 0x00000080
	wsExNoActivate        = 0x08000000
	hwndTopmost           = ^uintptr(0) // HWND_TOPMOST (-1)
	hwndNoTopmost         = ^uintptr(1) // HWND_NOTOPMOST (-2)
	swpNoActivate         = 0x0010
	swpShowWindow         = 0x0040
	swpHideWindow         = 0x0080
	swpNoMove             = 0x0002
	swpNoSize             = 0x0001
	wmDestroy             = 0x0002
	wmClose               = 0x0010
	wmTimer               = 0x0113
	wmApp                 = 0x8000
	ulwAlpha              = 0x00000002
	acSrcOver             = 0x00
	acSrcAlpha            = 0x01
	biRGB                 = 0
	dibRGBColors          = 0
	monitorDefaultNearest = 2
	timerID               = 1
	errorClassExists      = 1410 // ERROR_CLASS_ALREADY_EXISTS
)

var fontPaths = []string{
	filepath.Join(os.Getenv("WINDIR"), "Fonts", "segoeui.ttf"),
	filepath.Join(os.Getenv("WINDIR"), "Fonts", "arial.ttf"),
	filepath.Join(os.Getenv("WINDIR"), "Fonts", "tahoma.ttf"),
}

var (
	modUser32   = windows.NewLazySystemDLL("user32.dll")
	modGdi32    = windows.NewLazySystemDLL("gdi32.dll")
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW    = modUser32.NewProc("RegisterClassExW")
	procCreateWindowExW     = modUser32.NewProc("CreateWindowExW")
	procDestroyWindow       = modUser32.NewProc("DestroyWindow")
	procDefWindowProcW      = modUser32.NewProc("DefWindowProcW")
	procGetMessageW         = modUser32.NewProc("GetMessageW")
	procTranslateMessage    = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW    = modUser32.NewProc("DispatchMessageW")
	procPostMessageW        = modUser32.NewProc("PostMessageW")
	procPostQuitMessage     = modUser32.NewProc("PostQuitMessage")
	procSetTimer            = modUser32.NewProc("SetTimer")
	procKillTimer           = modUser32.NewProc("KillTimer")
	procSetWindowPos        = modUser32.NewProc("SetWindowPos")
	procGetCursorPos        = modUser32.NewProc("GetCursorPos")
	procMonitorFromPoint    = modUser32.NewProc("MonitorFromPoint")
	procGetMonitorInfoW     = modUser32.NewProc("GetMonitorInfoW")
	procUpdateLayeredWindow = modUser32.NewProc("UpdateLayeredWindow")
	procGetDC               = modUser32.NewProc("GetDC")
	procReleaseDC           = modUser32.NewProc("ReleaseDC")
	procGetModuleHandleW    = modKernel32.NewProc("GetModuleHandleW")
	procCreateCompatibleDC  = modGdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection    = modGdi32.NewProc("CreateDIBSection")
	procSelectObject        = modGdi32.NewProc("SelectObject")
	procDeleteObject        = modGdi32.NewProc("DeleteObject")
	procDeleteDC            = modGdi32.NewProc("DeleteDC")
)

var (
	classOnce sync.Once
	className = windows.StringToUTF16Ptr("vole-indicator")
	wndProcCb = windows.NewCallback(overlayWndProc)
	overlays  sync.Map // hwnd uintptr -> *Overlay
)

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

type winMsg struct {
	Hwnd    uintptr
	Message uint32
	_       uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	X, Y    int32
}

type winPoint struct{ X, Y int32 }
type winSize struct{ Cx, Cy int32 }

type blendFunction struct {
	BlendOp             byte
	BlendFlags          byte
	SourceConstantAlpha byte
	AlphaFormat         byte
}

type bitmapInfo struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type monitorInfo struct {
	CbSize  uint32
	Monitor windows.Rect
	Work    windows.Rect
	Flags   uint32
}

// Overlay is a click-through, no-activate layered window at the top of the
// monitor under the cursor. The tray icon is often hidden in the overflow, so
// this is the visible recording / transcribing indicator on Windows.
type Overlay struct {
	// hwnd is written by the UI thread (create, WM_DESTROY, teardown) and read
	// by every caller goroutine, so it is atomic rather than mu-guarded: the
	// UI thread already holds mu inside draw/onTimer.
	hwnd   atomic.Uintptr
	hdcMem uintptr
	bmp    uintptr
	oldBmp uintptr
	bits   unsafe.Pointer
	bmpW   int
	bmpH   int

	img  *image.NRGBA
	buf  []byte
	face font.Face

	mu        sync.Mutex
	mode      Mode
	lang      string
	level     float64
	progress  float64
	label     string
	toastText string
	toastAt   time.Time
	mapped    bool

	cmd       chan func()
	started   chan error
	done      chan struct{}
	closeOnce sync.Once
}

// New creates a hidden layered indicator window on a dedicated UI thread.
func New() (*Overlay, error) {
	o := &Overlay{
		img:     image.NewNRGBA(image.Rect(0, 0, winW, winH)),
		buf:     make([]byte, winW*winH*4),
		cmd:     make(chan func(), 8),
		started: make(chan error, 1),
		done:    make(chan struct{}),
	}
	o.loadFont()
	go o.thread()
	if err := <-o.started; err != nil {
		<-o.done
		return nil, err
	}
	fmt.Fprintln(os.Stderr, "[vole] overlay: Win32 layered indicator")
	return o, nil
}

func (o *Overlay) loadFont() {
	for _, p := range fontPaths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		ft, err := opentype.Parse(b)
		if err != nil {
			continue
		}
		face, err := opentype.NewFace(ft, &opentype.FaceOptions{
			Size: 15, DPI: 96, Hinting: font.HintingFull,
		})
		if err != nil {
			continue
		}
		o.face = face
		return
	}
}

func (o *Overlay) thread() {
	runtime.LockOSThread()
	defer close(o.done)
	if err := o.create(); err != nil {
		o.started <- err
		return
	}
	o.started <- nil
	procSetTimer.Call(o.hwnd.Load(), timerID, uintptr(framePeriod/time.Millisecond), 0)

	var m winMsg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	o.teardown()
}

func (o *Overlay) create() error {
	hinst, _, _ := procGetModuleHandleW.Call(0)
	classOnce.Do(func() {
		wc := wndClassEx{
			Size:      uint32(unsafe.Sizeof(wndClassEx{})),
			WndProc:   wndProcCb,
			Instance:  windows.Handle(hinst),
			ClassName: className,
		}
		atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		if atom == 0 {
			if errno, ok := err.(syscall.Errno); ok && errno == errorClassExists {
				return
			}
			fmt.Fprintln(os.Stderr, "vole overlay: RegisterClassEx:", err)
		}
	})

	ex := uintptr(wsExLayered | wsExTransparent | wsExToolwindow | wsExNoActivate)
	hwnd, _, err := procCreateWindowExW.Call(
		ex,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("vole"))),
		wsPopup,
		0, 0, winW, winH,
		0, 0, hinst, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowEx: %w", err)
	}
	o.hwnd.Store(hwnd)
	overlays.Store(hwnd, o)

	scr, _, err := procGetDC.Call(0)
	if scr == 0 {
		procDestroyWindow.Call(hwnd)
		overlays.Delete(hwnd)
		o.hwnd.Store(0)
		return fmt.Errorf("GetDC: %w", err)
	}
	mem, _, err := procCreateCompatibleDC.Call(scr)
	procReleaseDC.Call(0, scr)
	if mem == 0 {
		procDestroyWindow.Call(hwnd)
		overlays.Delete(hwnd)
		o.hwnd.Store(0)
		return fmt.Errorf("CreateCompatibleDC: %w", err)
	}
	o.hdcMem = mem
	if err := o.ensureDIB(winW, winH); err != nil {
		o.teardown()
		return err
	}
	return nil
}

func (o *Overlay) ensureDIB(w, h int) error {
	if o.bmpW == w && o.bmpH == h && o.bmp != 0 {
		return nil
	}
	if o.bmp != 0 {
		procSelectObject.Call(o.hdcMem, o.oldBmp)
		procDeleteObject.Call(o.bmp)
		o.bmp, o.oldBmp, o.bits = 0, 0, nil
	}
	bi := bitmapInfo{
		Size:        40,
		Width:       int32(w),
		Height:      -int32(h), // top-down
		Planes:      1,
		BitCount:    32,
		Compression: biRGB,
	}
	var bits unsafe.Pointer
	bmp, _, err := procCreateDIBSection.Call(o.hdcMem, uintptr(unsafe.Pointer(&bi)), dibRGBColors, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == nil {
		return fmt.Errorf("CreateDIBSection: %w", err)
	}
	prev, _, _ := procSelectObject.Call(o.hdcMem, bmp)
	o.bmp, o.oldBmp, o.bits = bmp, prev, bits
	o.bmpW, o.bmpH = w, h
	return nil
}

func (o *Overlay) teardown() {
	if hwnd := o.hwnd.Load(); hwnd != 0 {
		o.hwnd.Store(0)
		procKillTimer.Call(hwnd, timerID)
		overlays.Delete(hwnd)
		procDestroyWindow.Call(hwnd)
	}
	if o.bmp != 0 {
		procSelectObject.Call(o.hdcMem, o.oldBmp)
		procDeleteObject.Call(o.bmp)
		o.bmp = 0
		o.bits = nil
	}
	if o.hdcMem != 0 {
		procDeleteDC.Call(o.hdcMem)
		o.hdcMem = 0
	}
}

func overlayWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	v, ok := overlays.Load(hwnd)
	if !ok {
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
		return r
	}
	o := v.(*Overlay)
	switch msg {
	case wmApp:
		o.drainCmd()
		return 0
	case wmTimer:
		o.onTimer()
		return 0
	case wmClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		overlays.Delete(hwnd)
		o.hwnd.Store(0)
		procKillTimer.Call(hwnd, timerID)
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return r
}

func (o *Overlay) drainCmd() {
	for {
		select {
		case f := <-o.cmd:
			f()
		default:
			return
		}
	}
}

func (o *Overlay) onTimer() {
	o.mu.Lock()
	mode, at, mapped := o.mode, o.toastAt, o.mapped
	o.mu.Unlock()
	if !mapped {
		return
	}
	if mode == ModeToast && time.Since(at) >= toastDur {
		o.mu.Lock()
		o.mode = ModeHidden
		o.mapped = false
		o.mu.Unlock()
		o.hide()
		return
	}
	if mode == ModeHidden {
		return
	}
	o.draw()
}

func (o *Overlay) post(fn func()) {
	hwnd := o.hwnd.Load()
	if hwnd == 0 {
		return
	}
	select {
	case o.cmd <- fn:
		procPostMessageW.Call(hwnd, wmApp, 0, 0)
	default:
		// drop if the UI thread is backed up; the next timer tick still redraws
	}
}

func (o *Overlay) Show(lang string) {
	o.mu.Lock()
	o.mode = ModeRecording
	o.lang = lang
	o.level = 0
	o.mu.Unlock()
	o.post(func() { o.show(winW, winH) })
}

func (o *Overlay) SetLevel(l float64) {
	o.mu.Lock()
	o.level = l
	o.mu.Unlock()
}

func (o *Overlay) SetLang(lang string) {
	o.mu.Lock()
	o.lang = lang
	o.mu.Unlock()
}

func (o *Overlay) ShowDownload(label string) {
	o.mu.Lock()
	o.mode = ModeDownloading
	o.label = label
	o.progress = 0
	o.mu.Unlock()
	o.post(func() { o.show(winW, winH) })
}

func (o *Overlay) SetProgress(frac float64) {
	o.mu.Lock()
	o.progress = frac
	o.mu.Unlock()
}

func (o *Overlay) SetMode(m Mode) {
	o.mu.Lock()
	o.mode = m
	o.mu.Unlock()
}

func (o *Overlay) Toast(text string) {
	o.mu.Lock()
	o.mode = ModeToast
	o.toastText = text
	o.toastAt = time.Now()
	o.mu.Unlock()
	o.post(func() {
		w, h := toastSize(o.face, text)
		o.show(w, h)
	})
}

func (o *Overlay) Hide() {
	o.mu.Lock()
	o.mode = ModeHidden
	o.mu.Unlock()
	o.post(func() {
		o.mu.Lock()
		o.mapped = false
		o.mu.Unlock()
		o.hide()
	})
}

func (o *Overlay) Close() {
	o.closeOnce.Do(func() {
		hwnd := o.hwnd.Load()
		if hwnd == 0 {
			return
		}
		procPostMessageW.Call(hwnd, wmClose, 0, 0)
		<-o.done
	})
}

func (o *Overlay) show(w, h int) {
	o.resizeBuf(w, h)
	if err := o.ensureDIB(w, h); err != nil {
		fmt.Fprintln(os.Stderr, "vole overlay:", err)
		return
	}
	x, y := o.topCenter(w, h)
	procSetWindowPos.Call(o.hwnd.Load(), hwndTopmost, uintptr(x), uintptr(y), uintptr(w), uintptr(h), swpNoActivate|swpShowWindow)
	o.mu.Lock()
	o.mapped = true
	o.mu.Unlock()
	o.draw()
}

func (o *Overlay) hide() {
	// Drop TOPMOST while hidden so the tray context menu can stay open.
	procSetWindowPos.Call(o.hwnd.Load(), hwndNoTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate|swpHideWindow)
}

func (o *Overlay) resizeBuf(w, h int) {
	if o.img.Rect.Dx() == w && o.img.Rect.Dy() == h {
		return
	}
	o.img = image.NewNRGBA(image.Rect(0, 0, w, h))
	o.buf = make([]byte, w*h*4)
}

func (o *Overlay) topCenter(w, h int) (x, y int) {
	mx, my, mw, _ := currentMonitor()
	x = mx + (mw-w)/2
	if x < mx {
		x = mx
	}
	return x, my + topMargin
}

func currentMonitor() (x, y, w, h int) {
	var pt winPoint
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	mon, _, _ := procMonitorFromPoint.Call(uintptr(uint32(pt.X))|uintptr(uint32(pt.Y))<<32, monitorDefaultNearest)
	if mon == 0 {
		return 0, 0, 800, 600
	}
	mi := monitorInfo{CbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
	r, _, _ := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi)))
	if r == 0 {
		return 0, 0, 800, 600
	}
	rc := mi.Work
	return int(rc.Left), int(rc.Top), int(rc.Right - rc.Left), int(rc.Bottom - rc.Top)
}

func (o *Overlay) draw() {
	o.mu.Lock()
	st := paintState{
		mode: o.mode, lang: o.lang, level: o.level, progress: o.progress,
		label: o.label, toastText: o.toastText, toastAt: o.toastAt, face: o.face,
	}
	o.mu.Unlock()
	paint(o.img, st)
	o.flush()
}

func (o *Overlay) flush() {
	w, h := o.img.Rect.Dx(), o.img.Rect.Dy()
	if o.bits == nil || w != o.bmpW || h != o.bmpH {
		return
	}
	premulBGRA(o.buf, o.img.Pix)
	pix := unsafe.Slice((*byte)(o.bits), w*h*4)
	copy(pix, o.buf)

	var ptSrc winPoint
	sz := winSize{Cx: int32(w), Cy: int32(h)}
	blend := blendFunction{BlendOp: acSrcOver, SourceConstantAlpha: 255, AlphaFormat: acSrcAlpha}
	scr, _, _ := procGetDC.Call(0)
	if scr == 0 {
		return
	}
	procUpdateLayeredWindow.Call(
		o.hwnd.Load(),
		scr,
		0, // keep current position
		uintptr(unsafe.Pointer(&sz)),
		o.hdcMem,
		uintptr(unsafe.Pointer(&ptSrc)),
		0,
		uintptr(unsafe.Pointer(&blend)),
		ulwAlpha,
	)
	procReleaseDC.Call(0, scr)
}
