// Package service runs the server as two launchd instances that hand over to each other without
// downtime, and provides the listener they share.
package service

import (
	"context"
	"net"
	"syscall"
)

// Listen opens the TCP listener. With shared ports (set only by the launchd plist), SO_REUSEPORT
// lets the new instance listen on the same port before the old one leaves; a server started by hand
// never takes the service's traffic.
func Listen(ctx context.Context, addr string, shared bool) (net.Listener, error) {
	lc := net.ListenConfig{}
	if shared {
		lc.Control = func(network, address string, c syscall.RawConn) error {
			var serr error
			err := c.Control(func(fd uintptr) {
				serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEPORT, 1)
			})
			if err != nil {
				return err
			}
			return serr
		}
	}
	return lc.Listen(ctx, "tcp", addr)
}
