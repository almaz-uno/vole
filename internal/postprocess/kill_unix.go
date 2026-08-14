//go:build unix

package postprocess

import (
	"os/exec"
	"strings"
	"syscall"
)

func splitCommandLine(s string) ([]string, error) {
	return strings.Fields(s), nil
}

type unixKiller struct{}

func configureKill(cmd *exec.Cmd) killer {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // the whole group
		return cmd.Process.Kill()
	}
	return unixKiller{}
}
