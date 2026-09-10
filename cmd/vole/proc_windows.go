//go:build windows

package main

import (
	"syscall"

	"golang.org/x/sys/windows"
)

func daemonSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow: true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP |
			windows.DETACHED_PROCESS |
			windows.CREATE_NO_WINDOW,
	}
}
