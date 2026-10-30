//go:build linux

package util

import (
	"errors"
	"net"
	"time"

	"golang.org/x/sys/unix"
)

// KeepAliveFullSupported reports that keepalive interval and probe count can be
// set (Linux: TCP_KEEPINTVL, TCP_KEEPCNT).
const KeepAliveFullSupported = true

// ApplyTCPKeepAlive turns keepalive on and sets the idle time, the interval
// between probes and the number of failed probes. Go's SetKeepAlivePeriod only
// covers the idle time, so without the extra setsockopt a dead peer is noticed
// after the kernel default rather than after idle+interval*count.
func ApplyTCPKeepAlive(c *net.TCPConn, idle, intvl time.Duration, cnt int) error {
	if c == nil {
		return errors.New("not a TCP connection")
	}
	if err := c.SetKeepAlive(true); err != nil {
		return err
	}
	if idle > 0 {
		if err := c.SetKeepAlivePeriod(idle); err != nil {
			return err
		}
	}
	rc, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var sockErr error
	if err := rc.Control(func(fd uintptr) {
		f := int(fd)
		if idle > 0 {
			if e := unix.SetsockoptInt(f, unix.SOL_TCP, unix.TCP_KEEPIDLE, int(idle/time.Second)); e != nil {
				sockErr = e
				return
			}
		}
		if intvl > 0 {
			if e := unix.SetsockoptInt(f, unix.SOL_TCP, unix.TCP_KEEPINTVL, int(intvl/time.Second)); e != nil {
				sockErr = e
				return
			}
		}
		if cnt > 0 {
			if e := unix.SetsockoptInt(f, unix.SOL_TCP, unix.TCP_KEEPCNT, cnt); e != nil {
				sockErr = e
				return
			}
		}
	}); err != nil {
		return err
	}
	return sockErr
}
