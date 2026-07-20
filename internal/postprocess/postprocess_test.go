package postprocess

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeScript creates an executable +x script in a temp dir and returns its path.
func writeScript(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	// .sh suffix is cosmetic; exec runs it directly via its shebang.
	path := filepath.Join(dir, "pp.sh")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

func TestRun_Identity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	s := writeScript(t, "#!/bin/sh\ncat\n")
	got, err := Run(s, "hello world\n")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != "hello world" {
		t.Fatalf("got %q, want %q (trailing newline trimmed)", got, "hello world")
	}
}

func TestRun_Transforms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	s := writeScript(t, "#!/bin/sh\ntr '[:lower:]' '[:upper:]'\n")
	got, err := Run(s, "hello")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != "HELLO" {
		t.Fatalf("got %q, want HELLO", got)
	}
}

func TestRun_StdinReceivesFullText(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	// wc -c counts input bytes; we echo back to confirm the pipe carried text.
	s := writeScript(t, "#!/bin/sh\nsed 's/^/>> /'\n")
	in := "line one\nline two\n"
	got, err := Run(s, in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := ">> line one\n>> line two"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRun_ExitNonZero(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	s := writeScript(t, "#!/bin/sh\necho 'boom' >&2\nexit 1\n")
	_, err := Run(s, "x")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "post-process") {
		t.Fatalf("error should mention post-process: %v", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error should include stderr: %v", err)
	}
}

func TestRun_EmptyOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	s := writeScript(t, "#!/bin/sh\ntrue\n") // no stdout
	_, err := Run(s, "x")
	if err == nil || !strings.Contains(err.Error(), "empty output") {
		t.Fatalf("expected empty-output error, got: %v", err)
	}
}

func TestRun_WhitespaceOnlyOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	s := writeScript(t, "#!/bin/sh\necho '   \n  '\n")
	_, err := Run(s, "x")
	if err == nil || !strings.Contains(err.Error(), "empty output") {
		t.Fatalf("expected empty-output error, got: %v", err)
	}
}

func TestRun_MissingScript(t *testing.T) {
	_, err := Run(filepath.Join(t.TempDir(), "nope.sh"), "x")
	if err == nil {
		t.Fatal("expected error for missing script")
	}
}

func TestRun_Timeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	s := writeScript(t, "#!/bin/sh\nsleep 30\n")
	start := time.Now()
	_, err := RunTimeout(s, "x", 100*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("timeout did not fire promptly: %v", elapsed)
	}
}
