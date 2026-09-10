//go:build unix

package ipc

import (
	"net"
	"os"
)

// Dial connects to the daemon's unix socket.
func Dial(addr string) (net.Conn, error) {
	return net.Dial("unix", addr)
}

// Listen starts serving the daemon's unix socket, replacing a stale file.
func Listen(addr string) (net.Listener, error) {
	_ = os.Remove(addr)
	return net.Listen("unix", addr)
}

// Cleanup removes the socket file after the listener is closed.
func Cleanup(addr string) {
	_ = os.Remove(addr)
}

// Alive reports whether a daemon is already listening on addr.
func Alive(addr string) bool {
	c, err := net.Dial("unix", addr)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}
