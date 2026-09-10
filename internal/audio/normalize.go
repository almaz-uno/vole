package audio

// NormalizePeak scales samples so the peak absolute amplitude reaches target
// (typically 0.8). It is a no-op when the signal is silent, already loud
// enough, or would need more than maxGain. Returns the applied gain (>= 1).
func NormalizePeak(samples []float32, target, maxGain float32) float32 {
	if len(samples) == 0 || target <= 0 || maxGain < 1 {
		return 1
	}
	var peak float32
	for _, s := range samples {
		if s < 0 {
			s = -s
		}
		if s > peak {
			peak = s
		}
	}
	if peak == 0 || peak >= target {
		return 1
	}
	gain := target / peak
	if gain > maxGain {
		gain = maxGain
	}
	for i := range samples {
		samples[i] *= gain
	}
	return gain
}

// PadLeadingSilence prepends ms milliseconds of zeros. Whisper and Silero VAD
// often clip the first consonant of a PTT utterance; a short lead-in keeps it.
func PadLeadingSilence(samples []float32, ms int) []float32 {
	if len(samples) == 0 || ms <= 0 {
		return samples
	}
	n := SampleRate * ms / 1000
	out := make([]float32, n+len(samples))
	copy(out[n:], samples)
	return out
}
