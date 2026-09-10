package audio

import "testing"

func TestNormalizePeakBoostsQuiet(t *testing.T) {
	s := []float32{0.01, -0.02, 0.015}
	gain := NormalizePeak(s, 0.8, 64)
	if gain < 30 || gain > 50 {
		t.Fatalf("gain=%v want ~40", gain)
	}
	var peak float32
	for _, v := range s {
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	if peak < 0.79 || peak > 0.81 {
		t.Fatalf("peak after normalize=%v want 0.8", peak)
	}
}

func TestNormalizePeakNoOpWhenLoud(t *testing.T) {
	s := []float32{0.9, -0.5}
	if g := NormalizePeak(s, 0.8, 64); g != 1 {
		t.Fatalf("gain=%v want 1", g)
	}
	if s[0] != 0.9 {
		t.Fatalf("mutated loud sample: %v", s[0])
	}
}

func TestNormalizePeakCapsGain(t *testing.T) {
	s := []float32{0.001}
	gain := NormalizePeak(s, 0.8, 10)
	if gain != 10 {
		t.Fatalf("gain=%v want 10", gain)
	}
	if s[0] < 0.0099 || s[0] > 0.0101 {
		t.Fatalf("sample=%v want 0.01", s[0])
	}
}

func TestPadLeadingSilence(t *testing.T) {
	s := []float32{0.5, -0.5}
	got := PadLeadingSilence(s, 100)
	wantPad := SampleRate * 100 / 1000
	if len(got) != wantPad+2 {
		t.Fatalf("len=%d want %d", len(got), wantPad+2)
	}
	if got[0] != 0 || got[wantPad-1] != 0 {
		t.Fatalf("pad is not silence")
	}
	if got[wantPad] != 0.5 || got[wantPad+1] != -0.5 {
		t.Fatalf("speech shifted: %v %v", got[wantPad], got[wantPad+1])
	}
}

func TestNormalizePeakSilent(t *testing.T) {
	s := []float32{0, 0}
	if g := NormalizePeak(s, 0.8, 64); g != 1 {
		t.Fatalf("gain=%v want 1", g)
	}
}
