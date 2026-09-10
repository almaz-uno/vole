//go:build linux

package daemon

import "os/exec"

func notify(title, body string) {
	_ = exec.Command("notify-send", "-t", "2500", title, body).Run()
}

func prepareDaemonProcess() {}
