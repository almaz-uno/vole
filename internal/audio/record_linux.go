//go:build linux

package audio

import (
	"fmt"
	"io"
	"os/exec"
)

// Start begins capture from the PulseAudio/PipeWire default source via parec.
func (r *Recorder) Start() error {
	r.reset()

	cmd := exec.Command("parec",
		"--record",
		"--rate", fmt.Sprint(SampleRate),
		"--channels=1",
		"--format=s16le",
		"--latency-msec=30",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("parec: %w", err)
	}
	r.mu.Lock()
	r.cmd = cmd
	r.mu.Unlock()
	go r.readLoop(stdout)
	return nil
}

func (r *Recorder) readLoop(stdout io.ReadCloser) {
	defer close(r.done)
	chunk := make([]byte, 4096)
	for {
		n, err := stdout.Read(chunk)
		if n > 0 {
			r.appendPCM(chunk[:n])
		}
		if err != nil {
			return
		}
	}
}

// Stop stops capture and returns the accumulated audio as float32.
func (r *Recorder) Stop() []float32 {
	r.mu.Lock()
	cmd, _ := r.cmd.(*exec.Cmd)
	r.cmd = nil
	done := r.done
	r.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = cmd.Process.Kill()
	if done != nil {
		<-done
	}
	_ = cmd.Wait()
	return r.snapshot()
}
