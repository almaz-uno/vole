//go:build windows

package postprocess

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func splitCommandLine(s string) ([]string, error) {
	parts, err := windows.DecomposeCommandLine(s)
	if err != nil {
		return nil, fmt.Errorf("parse command line: %w", err)
	}
	return parts, nil
}

// jobKiller assigns the child to a Windows job object so a timeout kills the
// whole tree (powershell.exe -File … and whatever it spawned), not just the
// direct child.
type jobKiller struct {
	job windows.Handle
}

func configureKill(cmd *exec.Cmd) killer {
	k := &jobKiller{}
	job, err := windows.CreateJobObject(nil, nil)
	if err == nil {
		var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		_, err = windows.SetInformationJobObject(
			job,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)),
		)
		if err == nil {
			k.job = job
		} else {
			_ = windows.CloseHandle(job)
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
	cmd.Cancel = func() error {
		if k.job != 0 {
			_ = windows.TerminateJobObject(k.job, 1)
		}
		if cmd.Process != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	return k
}

func (k *jobKiller) afterStart(cmd *exec.Cmd) {
	if k.job == 0 || cmd.Process == nil {
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_ALL_ACCESS, false, uint32(cmd.Process.Pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	_ = windows.AssignProcessToJobObject(k.job, h)
}
