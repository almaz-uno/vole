package overlay

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
	"time"

	"github.com/almaz-uno/vole/internal/platform"
	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

// Mode is the indicator state. It aliases platform.Mode so *Overlay satisfies
// platform.Indicator directly.
type Mode = platform.Mode

const (
	ModeHidden         = platform.ModeHidden
	ModeRecording      = platform.ModeRecording
	ModeProcessing     = platform.ModeProcessing
	ModePostProcessing = platform.ModePostProcessing
	ModeDownloading    = platform.ModeDownloading
	ModeToast          = platform.ModeToast
)

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
	colPost  = color.NRGBA{33, 150, 243, 255} // blue — post-processing hook
	colText  = color.NRGBA{239, 240, 241, 255}
	colOK    = color.NRGBA{76, 175, 80, 255} // green check mark for the toast
	colVUoff = color.NRGBA{58, 58, 78, 255}
	colVUlo  = color.NRGBA{76, 175, 80, 255}
	colVUmid = color.NRGBA{255, 193, 7, 255}
	colVUhi  = color.NRGBA{229, 57, 53, 255}
)

type paintState struct {
	mode      Mode
	lang      string
	level     float64
	progress  float64
	label     string
	toastText string
	toastAt   time.Time
	face      font.Face
}

func paint(img *image.NRGBA, st paintState) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	clear(img.Pix)

	fade := 1.0
	if st.mode == ModeToast {
		if el := time.Since(st.toastAt); el > toastFade {
			fade = clamp(1-float64(el-toastFade)/float64(toastDur-toastFade), 0, 1)
		}
	}
	bg := colBG
	bg.A = uint8(float64(bg.A) * fade)
	fillRoundRect(img, 0, 0, w, h, radius, bg)

	switch st.mode {
	case ModeDownloading:
		drawDownload(img, st.face, st.label, st.progress)
	case ModeToast:
		drawToast(img, st.face, w, h, st.toastText, fade)
	default:
		drawRecording(img, st.face, st.lang, st.level, st.mode)
	}
}

func drawRecording(img *image.NRGBA, face font.Face, lang string, level float64, mode Mode) {
	icon := colRec
	if mode == ModeProcessing {
		icon = colProc
	} else if mode == ModePostProcessing {
		icon = colPost
	}
	fillCircle(img, 25, winH/2, 7.5, icon)

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
		fillRoundRect(img, vx+i*(segW+gap), vy, segW, segH, 1.5, c)
	}

	if face != nil && lang != "" {
		d := font.Drawer{
			Dst: img, Src: image.NewUniform(colText), Face: face,
			Dot: fixed.P(146, 30),
		}
		d.DrawString(strings.ToUpper(lang))
	}
}

func drawToast(img *image.NRGBA, face font.Face, w, h int, text string, fade float64) {
	ok := colOK
	ok.A = uint8(float64(ok.A) * fade)
	tx := colText
	tx.A = uint8(float64(tx.A) * fade)
	if face == nil {
		fillCircle(img, w/2, h/2, 8, ok)
		return
	}
	m := face.Metrics()
	baseY := (h-m.Height.Ceil())/2 + m.Ascent.Ceil()
	dc := font.Drawer{
		Dst: img, Src: image.NewUniform(ok), Face: face,
		Dot: fixed.P(toastPad, baseY),
	}
	dc.DrawString("✓")
	adv := font.MeasureString(face, "✓  ").Round()
	dt := font.Drawer{
		Dst: img, Src: image.NewUniform(tx), Face: face,
		Dot: fixed.P(toastPad+adv, baseY),
	}
	dt.DrawString(text)
}

func drawDownload(img *image.NRGBA, face font.Face, label string, progress float64) {
	if face != nil {
		d := font.Drawer{
			Dst: img, Src: image.NewUniform(colText), Face: face,
			Dot: fixed.P(14, 19),
		}
		d.DrawString("↓ " + label)

		pct := fmt.Sprintf("%d%%", int(clamp(progress, 0, 1)*100))
		w := font.MeasureString(face, pct).Round()
		dp := font.Drawer{
			Dst: img, Src: image.NewUniform(colText), Face: face,
			Dot: fixed.P(winW-14-w, 19),
		}
		dp.DrawString(pct)
	}
	const bx, by, bh = 14, 28, 9
	bw := winW - 2*bx
	fillRoundRect(img, bx, by, bw, bh, 4, colVUoff)
	if fw := int(float64(bw) * clamp(progress, 0, 1)); fw > 0 {
		fillRoundRect(img, bx, by, fw, bh, 4, colProc)
	}
}

func toastSize(face font.Face, text string) (int, int) {
	if face == nil {
		return winW, toastH
	}
	tw := font.MeasureString(face, "✓  "+text).Round()
	return tw + 2*toastPad, toastH
}

func premulBGRA(dst, src []byte) {
	for i := 0; i < len(src); i += 4 {
		r, g, b, a := uint32(src[i]), uint32(src[i+1]), uint32(src[i+2]), uint32(src[i+3])
		dst[i+0] = byte(b * a / 255)
		dst[i+1] = byte(g * a / 255)
		dst[i+2] = byte(r * a / 255)
		dst[i+3] = byte(a)
	}
}

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
