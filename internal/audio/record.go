package audio

import (
	"math"
	"sync"
)

// SampleRate is the capture rate required by whisper.
const SampleRate = 16000

// Recorder captures microphone audio as an s16le stream, accumulates the PCM,
// and concurrently tracks the signal level (RMS) for the indicator.
type Recorder struct {
	mu    sync.Mutex
	buf   []byte
	level float64 // smoothed RMS in the range [0,1]
	peak  float64 // peak instantaneous RMS for the session
	done  chan struct{}
	stop  chan struct{} // closed by Stop to end a native capture loop
	cmd   interface{}   // linux: *exec.Cmd for parec; unused elsewhere
}

func (r *Recorder) reset() {
	r.mu.Lock()
	r.buf = r.buf[:0]
	r.level = 0
	r.peak = 0
	r.done = make(chan struct{})
	r.stop = make(chan struct{})
	r.mu.Unlock()
}

func (r *Recorder) appendPCM(pcm []byte) {
	if len(pcm) == 0 {
		return
	}
	lvl := rms16(pcm)
	r.mu.Lock()
	r.buf = append(r.buf, pcm...)
	r.level = 0.6*r.level + 0.4*lvl
	if lvl > r.peak {
		r.peak = lvl
	}
	r.mu.Unlock()
}

// Level returns the current signal level [0,1] for the overlay/indicator.
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

func (r *Recorder) snapshot() []float32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return pcm16ToFloat32(r.buf)
}

func (r *Recorder) requestStop() <-chan struct{} {
	r.mu.Lock()
	if r.stop != nil {
		close(r.stop)
		r.stop = nil
	}
	done := r.done
	r.mu.Unlock()
	return done
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
