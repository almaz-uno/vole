//go:build linux

package main

import (
	"math"
	"time"

	"github.com/almaz-uno/vole/internal/overlay"
)

// cmdOverlayTest exercises the overlay in isolation, without recording or transcription.
func cmdOverlayTest() {
	ov, err := overlay.New()
	if err != nil {
		fatal(err)
	}
	defer ov.Close()

	ov.Show("RU")
	start := time.Now()
	for time.Since(start) < 4*time.Second {
		t := time.Since(start).Seconds()
		ov.SetLevel(0.02 + 0.13*(0.5+0.5*math.Sin(t*6))) // simulated speech level
		if t > 2.5 {
			ov.SetMode(overlay.ModeProcessing)
		}
		time.Sleep(40 * time.Millisecond)
	}
	ov.Hide()
	time.Sleep(300 * time.Millisecond)
}
