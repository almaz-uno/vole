package audio

import (
	"fmt"
	"io"
	"math"
	"os/exec"
	"sync"
)

// SampleRate is the capture rate required by whisper.
const SampleRate = 16000

// Recorder captures microphone audio (the PulseAudio/PipeWire default source)
// via parec as an s16le stream, accumulates the PCM, and concurrently tracks
// the signal level (RMS) for the overlay.
type Recorder struct {
	cmd *exec.Cmd

	mu    sync.Mutex
	buf   []byte
	level float64 // smoothed RMS in the range [0,1]
	peak  float64 // peak instantaneous RMS for the session
	done  chan struct{}
}

// Start begins capture. A background goroutine reads the stream until Stop.
func (r *Recorder) Start() error {
	r.mu.Lock()
	r.buf = r.buf[:0]
	r.level = 0
	r.peak = 0
	r.done = make(chan struct{})
	r.mu.Unlock()

	// parec: raw s16le mono 16k stream from the default device
	r.cmd = exec.Command("parec",
		"--record",
		"--rate", fmt.Sprint(SampleRate),
		"--channels=1",
		"--format=s16le",
		"--latency-msec=30",
	)
	stdout, err := r.cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := r.cmd.Start(); err != nil {
		return fmt.Errorf("parec: %w", err)
	}
	go r.readLoop(stdout)
	return nil
}

func (r *Recorder) readLoop(stdout io.ReadCloser) {
	defer close(r.done)
	chunk := make([]byte, 4096)
	for {
		n, err := stdout.Read(chunk)
		if n > 0 {
			lvl := rms16(chunk[:n])
			r.mu.Lock()
			r.buf = append(r.buf, chunk[:n]...)
			// exponential level smoothing
			r.level = 0.6*r.level + 0.4*lvl
			if lvl > r.peak {
				r.peak = lvl
			}
			r.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// Level returns the current signal level [0,1] for the overlay.
func (r *Recorder) Level() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.level
}

// Peak returns the peak level for the session — used to reject silence.
func (r *Recorder) Peak() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.peak
}

// Stop stops capture and returns the accumulated audio as float32.
func (r *Recorder) Stop() []float32 {
	if r.cmd == nil || r.cmd.Process == nil {
		return nil
	}
	_ = r.cmd.Process.Kill()
	<-r.done // wait for the read loop to finish
	_ = r.cmd.Wait()

	r.mu.Lock()
	defer r.mu.Unlock()
	return pcm16ToFloat32(r.buf)
}

// rms16 computes the normalized RMS of an s16le block.
func rms16(pcm []byte) float64 {
	n := len(pcm) / 2
	if n == 0 {
		return 0
	}
	var sum float64
	for i := 0; i < n; i++ {
		s := float64(int16(uint16(pcm[i*2]) | uint16(pcm[i*2+1])<<8))
		sum += s * s
	}
	return math.Sqrt(sum/float64(n)) / 32768.0
}
