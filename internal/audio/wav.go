// Package audio captures and decodes sound for transcription.
package audio

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
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

// WriteWAV16 writes samples to a 16-bit PCM mono WAV at SampleRate — the inverse
// of ReadWAV16, used to dump the raw captured audio for debugging. It creates the
// parent directory if needed.
func WriteWAV16(path string, samples []float32) error {
	pcm := make([]byte, len(samples)*2)
	for i, f := range samples {
		v := int32(math.Round(float64(f) * 32768))
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(int16(v)))
	}

	var buf bytes.Buffer
	const channels, bits = 1, 16
	byteRate := SampleRate * channels * bits / 8
	buf.WriteString("RIFF")
	binary.Write(&buf, binary.LittleEndian, uint32(36+len(pcm))) // RIFF chunk size
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	binary.Write(&buf, binary.LittleEndian, uint32(16))              // fmt chunk size
	binary.Write(&buf, binary.LittleEndian, uint16(1))               // PCM
	binary.Write(&buf, binary.LittleEndian, uint16(channels))        // channels
	binary.Write(&buf, binary.LittleEndian, uint32(SampleRate))      // sample rate
	binary.Write(&buf, binary.LittleEndian, uint32(byteRate))        // byte rate
	binary.Write(&buf, binary.LittleEndian, uint16(channels*bits/8)) // block align
	binary.Write(&buf, binary.LittleEndian, uint16(bits))            // bits per sample
	buf.WriteString("data")
	binary.Write(&buf, binary.LittleEndian, uint32(len(pcm))) // data chunk size
	buf.Write(pcm)

	if dir := filepath.Dir(path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
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
