package overlay

import (
	"image"
	"testing"
)

func TestPaintRecordingLeavesPixels(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, winW, winH))
	paint(img, paintState{mode: ModeRecording, lang: "ru", level: 0.4})
	opaque := 0
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] > 0 {
			opaque++
		}
	}
	if opaque < 100 {
		t.Fatalf("expected a visible panel, opaque pixels=%d", opaque)
	}
}

func TestPaintProcessingUsesAmber(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, winW, winH))
	paint(img, paintState{mode: ModeProcessing})
	// status dot is around (25, winH/2)
	c := img.NRGBAAt(25, winH/2)
	if int(c.R)+int(c.G) < 200 || c.B > 80 {
		t.Fatalf("processing dot = %#v, want amber", c)
	}
}

func TestClamp(t *testing.T) {
	if got := clamp(2, 0, 1); got != 1 {
		t.Fatalf("clamp high = %v", got)
	}
	if got := clamp(-1, 0, 1); got != 0 {
		t.Fatalf("clamp low = %v", got)
	}
}

func TestToastSizeWithoutFont(t *testing.T) {
	w, h := toastSize(nil, "Copied to clipboard")
	if w != winW || h != toastH {
		t.Fatalf("toastSize=%dx%d want %dx%d", w, h, winW, toastH)
	}
}
