//go:build windows

package tray

import (
	"encoding/binary"
	"image"
	"image/color"
	"testing"
)

func TestNRGBAToICOHeader(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	img.SetNRGBA(32, 32, color.NRGBA{120, 130, 150, 255})
	ico := nrgbaToICO(img)
	if len(ico) < 22+40 {
		t.Fatalf("ico too small: %d", len(ico))
	}
	if got := binary.LittleEndian.Uint16(ico[2:]); got != 1 {
		t.Fatalf("type=%d want 1 (icon)", got)
	}
	if ico[6] != 64 || ico[7] != 64 {
		t.Fatalf("entry size %dx%d want 64x64", ico[6], ico[7])
	}
	if got := binary.LittleEndian.Uint16(ico[12:]); got != 32 {
		t.Fatalf("bit count=%d want 32", got)
	}
	if got := binary.LittleEndian.Uint32(ico[18:]); got != 22 {
		t.Fatalf("image offset=%d want 22", got)
	}
	dib := ico[22:]
	if got := binary.LittleEndian.Uint32(dib[0:]); got != 40 {
		t.Fatalf("bih size=%d want 40", got)
	}
	if got := binary.LittleEndian.Uint32(dib[4:]); got != 64 {
		t.Fatalf("bih width=%d want 64", got)
	}
	if got := binary.LittleEndian.Uint32(dib[8:]); got != 128 {
		t.Fatalf("bih height=%d want 128 (xor+and)", got)
	}
	idle := stateIcon(colIdle)
	if len(idle) < 1000 {
		t.Fatalf("idle icon too small: %d", len(idle))
	}
	if idle[0] == 0x89 && idle[1] == 'P' {
		t.Fatal("idle icon is PNG; Windows tray needs ICO")
	}
}
