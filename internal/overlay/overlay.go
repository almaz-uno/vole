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
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/almaz-uno/vole/internal/platform"
	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/xproto"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Mode is the indicator state. It aliases platform.Mode so *Overlay satisfies
// platform.Indicator directly.
type Mode = platform.Mode

const (
	ModeHidden      = platform.ModeHidden
	ModeRecording   = platform.ModeRecording
	ModeProcessing  = platform.ModeProcessing
	ModeDownloading = platform.ModeDownloading
	ModeToast       = platform.ModeToast
)

var _ platform.Indicator = (*Overlay)(nil)

const (
	winW, winH  = 188, 46
	radius      = 12.0
	vuSegments  = 12
	levelGain   = 8.0
	topMargin   = 48 // px below the top screen edge (the overlay is centered-top)
	toastH      = 40 // height of the "copied" toast panel
	toastPad    = 16 // horizontal padding inside the toast
	toastDur    = 1900 * time.Millisecond
	toastFade   = 1400 * time.Millisecond // the toast starts fading out after this
	framePeriod = 33 * time.Millisecond
)

var (
	colBG    = color.NRGBA{30, 30, 46, 235}
	colRec   = color.NRGBA{229, 57, 53, 255}
	colProc  = color.NRGBA{255, 179, 0, 255}
	colText  = color.NRGBA{239, 240, 241, 255}
	colOK    = color.NRGBA{76, 175, 80, 255} // green check mark for the toast
	colVUoff = color.NRGBA{58, 58, 78, 255}
	colVUlo  = color.NRGBA{76, 175, 80, 255}
	colVUmid = color.NRGBA{255, 193, 7, 255}
	colVUhi  = color.NRGBA{229, 57, 53, 255}

	fontPaths = []string{
		"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
		"/usr/share/fonts/truetype/liberation/LiberationSans-Regular.ttf",
		"/usr/share/fonts/truetype/noto/NotoSans-Regular.ttf",
	}
)

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
		img:  image.NewNRGBA(image.Rect(0, 0, winW, winH)),
		buf:  make([]byte, winW*winH*4),
		cmd:  make(chan func(), 8),
		quit: make(chan struct{}),
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
		w, h := o.toastSize(text)
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

// toastSize measures the toast panel for text (loop goroutine: it uses the face).
func (o *Overlay) toastSize(text string) (int, int) {
	if o.face == nil {
		return winW, toastH
	}
	tw := font.MeasureString(o.face, "✓  "+text).Round()
	return tw + 2*toastPad, toastH
}

func (o *Overlay) draw() {
	o.mu.Lock()
	mode, lang, level, progress, label := o.mode, o.lang, o.level, o.progress, o.label
	toastText, toastAt := o.toastText, o.toastAt
	o.mu.Unlock()

	w, h := o.img.Rect.Dx(), o.img.Rect.Dy()
	clear(o.img.Pix) // transparent canvas

	// the toast fades its whole panel out over its last stretch
	fade := 1.0
	if mode == ModeToast {
		if el := time.Since(toastAt); el > toastFade {
			fade = clamp(1-float64(el-toastFade)/float64(toastDur-toastFade), 0, 1)
		}
	}
	bg := colBG
	bg.A = uint8(float64(bg.A) * fade)
	fillRoundRect(o.img, 0, 0, w, h, radius, bg)

	switch mode {
	case ModeDownloading:
		o.drawDownload(label, progress)
	case ModeToast:
		o.drawToast(w, h, toastText, fade)
	default: // recording / processing
		o.drawRecording(lang, level, mode)
	}

	o.flush()
}

// drawRecording renders the status icon, the VU meter, and the language label.
func (o *Overlay) drawRecording(lang string, level float64, mode Mode) {
	// status icon on the left
	icon := colRec
	if mode == ModeProcessing {
		icon = colProc
	}
	fillCircle(o.img, 25, winH/2, 7.5, icon)

	// VU meter
	filled := int(clamp(level*levelGain, 0, 1) * vuSegments)
	const vx, vy, segW, segH, gap = 42, 17, 6, 12, 2
	for i := 0; i < vuSegments; i++ {
		c := colVUoff
		if i < filled {
			switch {
			case i >= vuSegments-2:
				c = colVUhi
			case i >= vuSegments-5:
				c = colVUmid
			default:
				c = colVUlo
			}
		}
		fillRoundRect(o.img, vx+i*(segW+gap), vy, segW, segH, 1.5, c)
	}

	// language label
	if o.face != nil && lang != "" {
		d := font.Drawer{
			Dst: o.img, Src: image.NewUniform(colText), Face: o.face,
			Dot: fixed.P(146, 30),
		}
		d.DrawString(strings.ToUpper(lang))
	}
}

// drawToast renders a green check mark and the confirmation text, faded by [0,1].
func (o *Overlay) drawToast(w, h int, text string, fade float64) {
	ok := colOK
	ok.A = uint8(float64(ok.A) * fade)
	tx := colText
	tx.A = uint8(float64(tx.A) * fade)
	if o.face == nil {
		fillCircle(o.img, w/2, h/2, 8, ok)
		return
	}
	m := o.face.Metrics()
	baseY := (h-m.Height.Ceil())/2 + m.Ascent.Ceil()
	dc := font.Drawer{
		Dst: o.img, Src: image.NewUniform(ok), Face: o.face,
		Dot: fixed.P(toastPad, baseY),
	}
	dc.DrawString("✓")
	adv := font.MeasureString(o.face, "✓  ").Round()
	dt := font.Drawer{
		Dst: o.img, Src: image.NewUniform(tx), Face: o.face,
		Dot: fixed.P(toastPad+adv, baseY),
	}
	dt.DrawString(text)
}

// drawDownload renders the model name and a progress bar.
func (o *Overlay) drawDownload(label string, progress float64) {
	if o.face != nil {
		d := font.Drawer{
			Dst: o.img, Src: image.NewUniform(colText), Face: o.face,
			Dot: fixed.P(14, 19),
		}
		d.DrawString("↓ " + label)

		pct := fmt.Sprintf("%d%%", int(clamp(progress, 0, 1)*100))
		w := font.MeasureString(o.face, pct).Round()
		dp := font.Drawer{
			Dst: o.img, Src: image.NewUniform(colText), Face: o.face,
			Dot: fixed.P(winW-14-w, 19),
		}
		dp.DrawString(pct)
	}
	const bx, by, bh = 14, 28, 9
	bw := winW - 2*bx
	fillRoundRect(o.img, bx, by, bw, bh, 4, colVUoff)
	if fw := int(float64(bw) * clamp(progress, 0, 1)); fw > 0 {
		fillRoundRect(o.img, bx, by, fw, bh, 4, colProc)
	}
}

// flush: NRGBA → premultiplied BGRA, then PutImage to the window.
func (o *Overlay) flush() {
	w, h := o.img.Rect.Dx(), o.img.Rect.Dy()
	src := o.img.Pix
	for i := 0; i < len(src); i += 4 {
		r, g, b, a := uint32(src[i]), uint32(src[i+1]), uint32(src[i+2]), uint32(src[i+3])
		o.buf[i+0] = byte(b * a / 255) // B
		o.buf[i+1] = byte(g * a / 255) // G
		o.buf[i+2] = byte(r * a / 255) // R
		o.buf[i+3] = byte(a)           // A
	}
	xproto.PutImage(o.conn, xproto.ImageFormatZPixmap, xproto.Drawable(o.win), o.gc,
		uint16(w), uint16(h), 0, 0, 0, o.depth, o.buf)
}

// --- drawing primitives with edge antialiasing ---

func blend(img *image.NRGBA, x, y int, c color.NRGBA, cov float64) {
	if x < 0 || y < 0 || x >= img.Rect.Dx() || y >= img.Rect.Dy() || cov <= 0 {
		return
	}
	i := img.PixOffset(x, y)
	sa := float64(c.A) / 255 * cov
	da := float64(img.Pix[i+3]) / 255
	outA := sa + da*(1-sa)
	if outA <= 0 {
		return
	}
	mix := func(s, d float64) byte {
		return byte((s*sa + d*da*(1-sa)) / outA)
	}
	img.Pix[i+0] = mix(float64(c.R), float64(img.Pix[i+0]))
	img.Pix[i+1] = mix(float64(c.G), float64(img.Pix[i+1]))
	img.Pix[i+2] = mix(float64(c.B), float64(img.Pix[i+2]))
	img.Pix[i+3] = byte(outA * 255)
}

func fillRect(img *image.NRGBA, x, y, w, h int, c color.NRGBA) {
	for j := y; j < y+h; j++ {
		for i := x; i < x+w; i++ {
			blend(img, i, j, c, 1)
		}
	}
}

// fillRoundRect draws a rectangle with rounded corners (AA along the arcs).
func fillRoundRect(img *image.NRGBA, x, y, w, h int, r float64, c color.NRGBA) {
	if r < 0.5 {
		fillRect(img, x, y, w, h, c)
		return
	}
	fx, fy, fw, fh := float64(x), float64(y), float64(w), float64(h)
	for j := y; j < y+h; j++ {
		for i := x; i < x+w; i++ {
			px, py := float64(i)+0.5, float64(j)+0.5
			cov := 1.0
			// nearest corner-arc center
			var cx, cy float64
			corner := false
			switch {
			case px < fx+r && py < fy+r:
				cx, cy, corner = fx+r, fy+r, true
			case px > fx+fw-r && py < fy+r:
				cx, cy, corner = fx+fw-r, fy+r, true
			case px < fx+r && py > fy+fh-r:
				cx, cy, corner = fx+r, fy+fh-r, true
			case px > fx+fw-r && py > fy+fh-r:
				cx, cy, corner = fx+fw-r, fy+fh-r, true
			}
			if corner {
				d := math.Hypot(px-cx, py-cy)
				cov = clamp(r-d+0.5, 0, 1)
			}
			blend(img, i, j, c, cov)
		}
	}
}

// fillCircle draws a filled circle (AA along the edge).
func fillCircle(img *image.NRGBA, cx, cy int, r float64, c color.NRGBA) {
	x0, y0 := cx-int(r)-1, cy-int(r)-1
	x1, y1 := cx+int(r)+1, cy+int(r)+1
	fcx, fcy := float64(cx), float64(cy)
	for j := y0; j <= y1; j++ {
		for i := x0; i <= x1; i++ {
			d := math.Hypot(float64(i)+0.5-fcx, float64(j)+0.5-fcy)
			blend(img, i, j, c, clamp(r-d+0.5, 0, 1))
		}
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
