//go:build !windows

package tray

import (
	"bytes"
	"image"
	"image/png"
)

func trayIconBytes(img *image.NRGBA) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
