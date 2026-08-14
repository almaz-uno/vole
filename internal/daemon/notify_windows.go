//go:build windows

package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"
)

func notify(title, body string) {
	fmt.Fprintf(os.Stderr, "[vole] %s — %s\n", title, body)
	go windowsToast(title, body)
}

// windowsToast shows a Windows toast via PowerShell WinRT. Failures are
// silent: the stderr line above is the guaranteed fallback.
func windowsToast(title, body string) {
	xml := fmt.Sprintf(
		`<toast><visual><binding template="ToastGeneric"><text>%s</text><text>%s</text></binding></visual></toast>`,
		xmlEscape(title), xmlEscape(body),
	)
	ps := fmt.Sprintf(`
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] | Out-Null
$xml = New-Object Windows.Data.Xml.Dom.XmlDocument
$xml.LoadXml(%s)
$toast = [Windows.UI.Notifications.ToastNotification]::new($xml)
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('vole').Show($toast)
`, psSingleQuote(xml))
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", ps)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Run()
}

func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}

func psSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func prepareDaemonProcess() {
	hideOwnedConsole()
}

var (
	modKernel32               = syscall.NewLazyDLL("kernel32.dll")
	modUser32                 = syscall.NewLazyDLL("user32.dll")
	procGetConsoleWindow      = modKernel32.NewProc("GetConsoleWindow")
	procGetConsoleProcessList = modKernel32.NewProc("GetConsoleProcessList")
	procShowWindow            = modUser32.NewProc("ShowWindow")
)

const swHide = 0

func hideOwnedConsole() {
	var pids [4]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), 4)
	if n != 1 {
		return // shared console (cmd.exe) or none — leave it alone
	}
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd != 0 {
		procShowWindow.Call(hwnd, swHide)
	}
}
