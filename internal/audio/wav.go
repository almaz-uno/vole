// Package audio captures and decodes sound for transcription.
package audio

import (
	"encoding/binary"
	"fmt"
	"os"
)

// ReadWAV16 reads a 16-bit PCM WAV (expected 16 kHz mono) and returns the
// samples as float32 in the range [-1, 1]. Minimal RIFF parser.
func ReadWAV16(path string) ([]float32, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < 44 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, fmt.Errorf("audio: %q does not look like a WAV", path)
	}

	pos := 12
	for pos+8 <= len(data) {
		id := string(data[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		body := pos + 8
		if id == "data" {
			end := body + size
			if end > len(data) {
				end = len(data)
			}
			return pcm16ToFloat32(data[body:end]), nil
		}
		pos = body + size + (size & 1) // chunks are word-aligned
	}
	return nil, fmt.Errorf("audio: no data chunk found in %q", path)
}

// PCM16ToFloat32 converts a raw s16le stream to float32 [-1, 1].
func PCM16ToFloat32(pcm []byte) []float32 {
	return pcm16ToFloat32(pcm)
}

func pcm16ToFloat32(pcm []byte) []float32 {
	n := len(pcm) / 2
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		s := int16(binary.LittleEndian.Uint16(pcm[i*2:]))
		out[i] = float32(s) / 32768.0
	}
	return out
}
