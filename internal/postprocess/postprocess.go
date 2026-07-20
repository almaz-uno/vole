// Package postprocess runs an external script that cleans up a transcript: the
// raw whisper text goes on the script's stdin and the improved text comes back
// on stdout. The daemon calls Run only when post-processing is enabled; on any
// error or empty output it falls back to the raw transcript, so a broken script
// never blocks dictation.
package postprocess

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// defaultTimeout bounds a run: a hung script must not stall dictation. The whole
// process group is SIGKILLed when the deadline elapses.
const defaultTimeout = 15 * time.Second

// pipeDrain is how long Run waits for the script to release its stdout/stderr
// pipes after being killed — a safety net so a grandchild holding the pipe
// (e.g. a shell that forked `sleep`) cannot keep Run blocked.
const pipeDrain = 500 * time.Millisecond

// Run executes script with text on stdin and returns the improved text from
// stdout. A single trailing newline is trimmed. It returns an error — so the
// caller can fall back to the raw transcript — if the script is missing, exits
// non-zero, times out, or returns empty/whitespace-only output.
//
// script is executed directly (not via a shell), so it must be executable and
// carry its own shebang (e.g. #!/bin/sh, #!/usr/bin/env python3).
func Run(script, text string) (string, error) {
	return RunTimeout(script, text, defaultTimeout)
}

// RunTimeout is Run with an explicit deadline, exposed for tests.
func RunTimeout(script, text string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, script)
	cmd.Stdin = strings.NewReader(text)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Put the script in its own process group so a timeout can SIGKILL the whole
	// group — not just the direct child. Otherwise a shell that forks its command
	// (a grandchild holding the stdout pipe) survives a child-only kill and keeps
	// Run blocked until the grandchild exits on its own.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // the whole group
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = pipeDrain

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("post-process: %s timed out after %s", script, timeout)
		}
		sep := ""
		if s := strings.TrimSpace(stderr.String()); s != "" {
			sep = ": " + s
		}
		return "", fmt.Errorf("post-process: %s: %w%s", script, err, sep)
	}
	out := strings.TrimSuffix(strings.TrimSuffix(stdout.String(), "\n"), "\r")
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("post-process: %s returned empty output", script)
	}
	return out, nil
}
