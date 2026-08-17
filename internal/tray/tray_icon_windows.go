//go:build windows

package tray

import (
	"encoding/binary"
	"image"
)

// trayIconBytes encodes img as a 32-bit ICO. fyne.io/systray on Windows loads
// the tray image with LoadImage(IMAGE_ICON); a PNG file is rejected.
func trayIconBytes(img *image.NRGBA) []byte {
	return nrgbaToICO(img)
}

func nrgbaToICO(img *image.NRGBA) []byte {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if w > 256 {
		w = 256
	}
	if h > 256 {
		h = 256
	}
	andRow := ((w + 31) / 32) * 4
	xorSize := w * h * 4
	andSize := andRow * h
	dibSize := 40 + xorSize + andSize

	out := make([]byte, 22+dibSize)
	// ICONDIR
	binary.LittleEndian.PutUint16(out[0:], 0)
	binary.LittleEndian.PutUint16(out[2:], 1) // icon
	binary.LittleEndian.PutUint16(out[4:], 1) // one image
	// ICONDIRENTRY
	out[6] = byte(w) // 0 means 256
	out[7] = byte(h)
	out[8] = 0
	out[9] = 0
	binary.LittleEndian.PutUint16(out[10:], 1)  // planes
	binary.LittleEndian.PutUint16(out[12:], 32) // bit count
	binary.LittleEndian.PutUint32(out[14:], uint32(dibSize))
	binary.LittleEndian.PutUint32(out[18:], 22)

	dib := out[22:]
	binary.LittleEndian.PutUint32(dib[0:], 40) // BITMAPINFOHEADER
	binary.LittleEndian.PutUint32(dib[4:], uint32(w))
	binary.LittleEndian.PutUint32(dib[8:], uint32(h*2)) // XOR + AND
	binary.LittleEndian.PutUint16(dib[12:], 1)
	binary.LittleEndian.PutUint16(dib[14:], 32)
	binary.LittleEndian.PutUint32(dib[20:], uint32(xorSize))

	xor := dib[40:]
	and := dib[40+xorSize:]
	for y := 0; y < h; y++ {
		srcY := h - 1 - y // DIB is bottom-up
		for x := 0; x < w; x++ {
			c := img.NRGBAAt(img.Bounds().Min.X+x, img.Bounds().Min.Y+srcY)
			i := (y*w + x) * 4
			xor[i+0] = c.B
			xor[i+1] = c.G
			xor[i+2] = c.R
			xor[i+3] = c.A
			if c.A == 0 {
				and[y*andRow+x/8] |= 1 << (7 - uint(x%8))
			}
		}
	}
	return out
}
