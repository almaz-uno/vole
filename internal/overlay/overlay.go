// Package overlay is a floating recording indicator near the cursor: a
// borderless ARGB32 override-redirect window (transparency via the compositor)
// that draws a rounded panel with a status icon, a VU meter, and a language label.
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
)

var _ platform.Indicator = (*Overlay)(nil)

const (
	winW, winH  = 188, 46
	radius      = 12.0
	vuSegments  = 12
	levelGain   = 8.0
	cursorDX    = 18
	cursorDY    = 18
	framePeriod = 33 * time.Millisecond
)

var (
	colBG    = color.NRGBA{30, 30, 46, 235}
	colRec   = color.NRGBA{229, 57, 53, 255}
	colProc  = color.NRGBA{255, 179, 0, 255}
	colText  = color.NRGBA{239, 240, 241, 255}
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
	conn  *xgb.Conn
	win   xproto.Window
	gc    xproto.Gcontext
	root  xproto.Window
	depth byte
	face  font.Face

	img *image.NRGBA
	buf []byte

	mu       sync.Mutex
	mode     Mode
	lang     string
	level    float64
	progress float64 // download progress [0,1]
	label    string  // download label (model name)

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
			visible := o.mode != ModeHidden
			o.mu.Unlock()
			if visible {
				o.draw()
			}
		case <-o.quit:
			return
		}
	}
}

// Show displays the indicator near the cursor in recording mode.
func (o *Overlay) Show(lang string) {
	o.mu.Lock()
	o.mode = ModeRecording
	o.lang = lang
	o.level = 0
	o.mu.Unlock()
	o.cmd <- func() {
		o.positionAtCursor()
		xproto.MapWindow(o.conn, o.win)
		xproto.ConfigureWindow(o.conn, o.win,
			xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove})
		o.draw()
	}
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
	o.cmd <- func() {
		o.positionAtCursor()
		xproto.MapWindow(o.conn, o.win)
		xproto.ConfigureWindow(o.conn, o.win,
			xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove})
		o.draw()
	}
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

func (o *Overlay) positionAtCursor() {
	reply, err := xproto.QueryPointer(o.conn, o.root).Reply()
	if err != nil {
		return
	}
	xproto.ConfigureWindow(o.conn, o.win,
		xproto.ConfigWindowX|xproto.ConfigWindowY,
		[]uint32{uint32(int(reply.RootX) + cursorDX), uint32(int(reply.RootY) + cursorDY)})
}

func (o *Overlay) draw() {
	o.mu.Lock()
	mode, lang, level, progress, label := o.mode, o.lang, o.level, o.progress, o.label
	o.mu.Unlock()

	clear(o.img.Pix) // transparent canvas

	// rounded backdrop
	fillRoundRect(o.img, 0, 0, winW, winH, radius, colBG)

	if mode == ModeDownloading {
		o.drawDownload(label, progress)
		o.flush()
		return
	}

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

	o.flush()
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
	src := o.img.Pix
	for i := 0; i < len(src); i += 4 {
		r, g, b, a := uint32(src[i]), uint32(src[i+1]), uint32(src[i+2]), uint32(src[i+3])
		o.buf[i+0] = byte(b * a / 255) // B
		o.buf[i+1] = byte(g * a / 255) // G
		o.buf[i+2] = byte(r * a / 255) // R
		o.buf[i+3] = byte(a)           // A
	}
	xproto.PutImage(o.conn, xproto.ImageFormatZPixmap, xproto.Drawable(o.win), o.gc,
		winW, winH, 0, 0, 0, o.depth, o.buf)
}

// --- drawing primitives with edge antialiasing ---

func blend(img *image.NRGBA, x, y int, c color.NRGBA, cov float64) {
	if x < 0 || y < 0 || x >= winW || y >= winH || cov <= 0 {
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
