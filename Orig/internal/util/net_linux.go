//go:build linux

package util

import (
	"context"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

func ListenUDPReuse(addr string) (*net.UDPConn, error) {
	lc := &net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var err error
			c.Control(func(fd uintptr) {
				err = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
			})
			return err
		},
	}
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	c, err := lc.ListenPacket(context.Background(), "udp", ua.String())
	if err != nil {
		return nil, err
	}
	return c.(*net.UDPConn), nil
}

func ListenTCPReuse(addr string) (net.Listener, error) {
	lc := &net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var err error
			c.Control(func(fd uintptr) {
				err = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
			})
			return err
		},
	}
	return lc.Listen(context.Background(), "tcp", addr)
}