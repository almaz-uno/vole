//go:build linux

// Package overlay is a floating indicator centered near the top of the screen:
// a borderless ARGB32 override-redirect window (transparency via the compositor)
// that draws a rounded panel with a status icon, a VU meter, and a language
// label while recording. After a tray-click dictation it also shows a brief,
// fading "copied to clipboard" toast in the same place.
//
// All rendering goes into an image.NRGBA and is blitted to the window via PutImage.
// The X11 connection is not thread-safe, so all requests run from the loop goroutine.
package overlay

import (
	"image"
	"os"
	"sync"
	"time"

	"github.com/almaz-uno/vole/internal/platform"
	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/xproto"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
)

var _ platform.Indicator = (*Overlay)(nil)

var fontPaths = []string{
	"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
	"/usr/share/fonts/truetype/liberation/LiberationSans-Regular.ttf",
	"/usr/share/fonts/truetype/noto/NotoSans-Regular.ttf",
}

// Overlay is the managed indicator. Created with New, released with Close.
type Overlay struct {
	conn             *xgb.Conn
	win              xproto.Window
	gc               xproto.Gcontext
	root             xproto.Window
	depth            byte
	face             font.Face
	screenW, screenH int
	hasRandr         bool // RandR available → center on the monitor under the cursor

	img *image.NRGBA
	buf []byte

	mu        sync.Mutex
	mode      Mode
	lang      string
	level     float64
	progress  float64 // download progress [0,1]
	label     string  // download label (model name)
	toastText string  // toast text (ModeToast)
	toastAt   time.Time

	cmd  chan func()
	quit chan struct{}
}

// New connects to the X server and creates a hidden ARGB indicator window.
func New() (*Overlay, error) {
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, err
	}
	setup := xproto.Setup(conn)
	screen := setup.DefaultScreen(conn)

	// 32-bit visual for true transparency; otherwise fall back to root
	var vis xproto.Visualid
	var depth byte
	for _, d := range screen.AllowedDepths {
		if d.Depth == 32 && len(d.Visuals) > 0 {
			vis = d.Visuals[0].VisualId
			depth = 32
			break
		}
	}
	if depth == 0 {
		vis = screen.RootVisual
		depth = screen.RootDepth
	}

	cmap, _ := xproto.NewColormapId(conn)
	xproto.CreateColormap(conn, xproto.ColormapAllocNone, cmap, screen.Root, vis)

	win, _ := xproto.NewWindowId(conn)
	mask := uint32(xproto.CwBackPixel | xproto.CwBorderPixel |
		xproto.CwOverrideRedirect | xproto.CwColormap)
	vals := []uint32{0, 0, 1, uint32(cmap)} // transparent background, no border, override, colormap
	if err := xproto.CreateWindowChecked(conn, depth, win, screen.Root,
		0, 0, winW, winH, 0, xproto.WindowClassInputOutput, vis,
		mask, vals).Check(); err != nil {
		conn.Close()
		return nil, err
	}

	gc, _ := xproto.NewGcontextId(conn)
	xproto.CreateGC(conn, gc, xproto.Drawable(win), 0, nil)

	o := &Overlay{
		conn: conn, win: win, gc: gc, root: screen.Root, depth: depth,
		screenW: int(screen.WidthInPixels), screenH: int(screen.HeightInPixels),
		hasRandr: randr.Init(conn) == nil, // for per-monitor centering on a multi-head X screen
		img:      image.NewNRGBA(image.Rect(0, 0, winW, winH)),
		buf:      make([]byte, winW*winH*4),
		cmd:      make(chan func(), 8),
		quit:     make(chan struct{}),
	}
	o.loadFont()
	go o.loop()
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

func (o *Overlay) loop() {
	tick := time.NewTicker(framePeriod)
	defer tick.Stop()
	for {
		select {
		case f := <-o.cmd:
			f()
		case <-tick.C:
			o.mu.Lock()
			mode, at := o.mode, o.toastAt
			o.mu.Unlock()
			switch {
			case mode == ModeHidden:
				// nothing to draw
			case mode == ModeToast && time.Since(at) >= toastDur:
				o.mu.Lock()
				o.mode = ModeHidden
				o.mu.Unlock()
				xproto.UnmapWindow(o.conn, o.win)
			default:
				o.draw()
			}
		case <-o.quit:
			return
		}
	}
}

// Show displays the indicator (centered-top) in recording mode.
func (o *Overlay) Show(lang string) {
	o.mu.Lock()
	o.mode = ModeRecording
	o.lang = lang
	o.level = 0
	o.mu.Unlock()
	o.cmd <- func() { o.show(winW, winH) }
}

// SetLevel updates the signal level [0,1] for the VU meter.
func (o *Overlay) SetLevel(l float64) {
	o.mu.Lock()
	o.level = l
	o.mu.Unlock()
}

// SetLang changes the language label on the fly (Shift toggling during recording).
func (o *Overlay) SetLang(lang string) {
	o.mu.Lock()
	o.lang = lang
	o.mu.Unlock()
}

// ShowDownload displays the indicator in download mode with the given label.
func (o *Overlay) ShowDownload(label string) {
	o.mu.Lock()
	o.mode = ModeDownloading
	o.label = label
	o.progress = 0
	o.mu.Unlock()
	o.cmd <- func() { o.show(winW, winH) }
}

// SetProgress updates the download progress [0,1].
func (o *Overlay) SetProgress(frac float64) {
	o.mu.Lock()
	o.progress = frac
	o.mu.Unlock()
}

// SetMode switches the mode (for example, to transcribing).
func (o *Overlay) SetMode(m Mode) {
	o.mu.Lock()
	o.mode = m
	o.mu.Unlock()
}

// Toast shows a brief, fading "copied to clipboard" confirmation centered-top.
// It dismisses itself after toastDur (see loop).
func (o *Overlay) Toast(text string) {
	o.mu.Lock()
	o.mode = ModeToast
	o.toastText = text
	o.toastAt = time.Now()
	o.mu.Unlock()
	o.cmd <- func() {
		w, h := toastSize(o.face, text)
		o.show(w, h)
	}
}

// Hide hides the indicator.
func (o *Overlay) Hide() {
	o.mu.Lock()
	o.mode = ModeHidden
	o.mu.Unlock()
	o.cmd <- func() { xproto.UnmapWindow(o.conn, o.win) }
}

// Close hides the window and closes the connection.
func (o *Overlay) Close() {
	close(o.quit)
	xproto.DestroyWindow(o.conn, o.win)
	o.conn.Close()
}

// show resizes the backing image/window to w×h, centers it near the top of the
// screen, maps it above other windows, and draws the current state. Runs on the
// loop goroutine (it touches conn and img/buf).
func (o *Overlay) show(w, h int) {
	o.resizeBuf(w, h)
	mx, my, mw, _ := o.currentMonitor()
	x := mx + (mw-w)/2
	if x < mx {
		x = mx
	}
	y := my + topMargin
	xproto.ConfigureWindow(o.conn, o.win,
		xproto.ConfigWindowX|xproto.ConfigWindowY|xproto.ConfigWindowWidth|xproto.ConfigWindowHeight,
		[]uint32{uint32(x), uint32(y), uint32(w), uint32(h)})
	xproto.MapWindow(o.conn, o.win)
	xproto.ConfigureWindow(o.conn, o.win,
		xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove})
	o.draw()
}

// currentMonitor returns the geometry of the monitor under the mouse pointer —
// the X "screen" can span several physical monitors, so centering on its full
// width lands the overlay on the seam between them. Falls back to the primary
// monitor, then to the whole virtual screen.
func (o *Overlay) currentMonitor() (x, y, w, h int) {
	x, y, w, h = 0, 0, o.screenW, o.screenH
	if !o.hasRandr {
		return
	}
	rep, err := randr.GetMonitors(o.conn, o.root, true).Reply()
	if err != nil || len(rep.Monitors) == 0 {
		return
	}
	px, py := -1, -1
	if p, err := xproto.QueryPointer(o.conn, o.root).Reply(); err == nil {
		px, py = int(p.RootX), int(p.RootY)
	}
	var primary *randr.MonitorInfo
	for i := range rep.Monitors {
		m := &rep.Monitors[i]
		if int(m.X) <= px && px < int(m.X)+int(m.Width) &&
			int(m.Y) <= py && py < int(m.Y)+int(m.Height) {
			return int(m.X), int(m.Y), int(m.Width), int(m.Height)
		}
		if m.Primary {
			primary = m
		}
	}
	if primary != nil {
		return int(primary.X), int(primary.Y), int(primary.Width), int(primary.Height)
	}
	m := &rep.Monitors[0]
	return int(m.X), int(m.Y), int(m.Width), int(m.Height)
}

// resizeBuf reallocates the backing image/buffer when the panel size changes.
// Called only from the loop goroutine, which owns img/buf.
func (o *Overlay) resizeBuf(w, h int) {
	if o.img.Rect.Dx() == w && o.img.Rect.Dy() == h {
		return
	}
	o.img = image.NewNRGBA(image.Rect(0, 0, w, h))
	o.buf = make([]byte, w*h*4)
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

// flush: NRGBA → premultiplied BGRA, then PutImage to the window.
func (o *Overlay) flush() {
	w, h := o.img.Rect.Dx(), o.img.Rect.Dy()
	premulBGRA(o.buf, o.img.Pix)
	xproto.PutImage(o.conn, xproto.ImageFormatZPixmap, xproto.Drawable(o.win), o.gc,
		uint16(w), uint16(h), 0, 0, 0, o.depth, o.buf)
}
