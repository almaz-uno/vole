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
	"os"
	"os/exec"
	"strings"
	"time"
)

// defaultTimeout bounds a run: a hung script must not stall dictation. The whole
// process group (unix) or job object (Windows) is killed when the deadline elapses.
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
// script is executed directly (not via a shell). On Unix it must be executable
// and carry its own shebang. On Windows a path to a .exe works as-is; a command
// line such as `powershell.exe -NoProfile -File C:\path\vole-post.ps1` is split
// into argv when the whole string is not an existing file.
func Run(script, text string) (string, error) {
	return RunTimeout(script, text, defaultTimeout)
}

// RunTimeout is Run with an explicit deadline, exposed for tests.
func RunTimeout(script, text string, timeout time.Duration) (string, error) {
	return RunTimeoutEnv(script, text, timeout, nil)
}

// RunTimeoutEnv is RunTimeout with extra environment variables appended to the
// daemon's environment (e.g. VOLE_PP_TRANSLATE=1 to switch the script into its
// translate-to-English mode). env may be nil.
func RunTimeoutEnv(script, text string, timeout time.Duration, env []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	name, args, err := parseCommand(script)
	if err != nil {
		return "", fmt.Errorf("post-process: %w", err)
	}

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader(text)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	killer := configureKill(cmd)
	cmd.WaitDelay = pipeDrain

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("post-process: %s: %w", script, err)
	}
	killer.afterStart(cmd)
	if err := cmd.Wait(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("post-process: %s timed out after %s", script, timeout)
		}
		sep := ""
		if s := strings.TrimSpace(stderr.String()); s != "" {
			sep = ": " + s
		}
		return "", fmt.Errorf("post-process: %s: %w%s", script, err, sep)
	}
	out := strings.TrimRight(stdout.String(), "\r\n")
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("post-process: %s returned empty output", script)
	}
	return out, nil
}

func parseCommand(script string) (name string, args []string, err error) {
	script = strings.TrimSpace(script)
	if script == "" {
		return "", nil, fmt.Errorf("empty command")
	}
	if fi, err := os.Stat(script); err == nil && !fi.IsDir() {
		return script, nil, nil
	}
	parts, err := splitCommandLine(script)
	if err != nil {
		return "", nil, err
	}
	if len(parts) == 0 {
		return "", nil, fmt.Errorf("empty command")
	}
	return parts[0], parts[1:], nil
}

// killer tears down a timed-out post-process tree.
type killer interface {
	afterStart(*exec.Cmd)
}

type nopKiller struct{}

func (nopKiller) afterStart(*exec.Cmd) {}
