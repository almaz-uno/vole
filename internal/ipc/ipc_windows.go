//go:build windows

package ipc

import (
	"net"
	"time"

	"github.com/Microsoft/go-winio"
)

// Dial connects to the daemon's named pipe (e.g. \\.\pipe\vole).
func Dial(addr string) (net.Conn, error) {
	timeout := 2 * time.Second
	return winio.DialPipe(addr, &timeout)
}

// Listen starts serving the daemon's named pipe.
func Listen(addr string) (net.Listener, error) {
	return winio.ListenPipe(addr, &winio.PipeConfig{
		InputBufferSize:  65536,
		OutputBufferSize: 65536,
	})
}

// Cleanup is a no-op: a named pipe vanishes when the listener closes.
func Cleanup(string) {}

// Alive reports whether a daemon is already listening on addr.
func Alive(addr string) bool {
	timeout := 200 * time.Millisecond
	c, err := winio.DialPipe(addr, &timeout)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}
