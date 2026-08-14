//go:build windows

package main

import (
	"fmt"
	"os"
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

func cmdOverlayTest() {
	fmt.Fprintln(os.Stderr, "vole: overlay-test is not available on Windows (no floating overlay)")
	os.Exit(1)
}
