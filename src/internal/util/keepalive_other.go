//go:build !linux

package util

import (
	"errors"
	"net"
	"time"
)

// KeepAliveFullSupported reports that the keepalive interval and probe count
// from the config are not applied on this platform; only the idle time is,
// through the portable SetKeepAlivePeriod.
const KeepAliveFullSupported = false

// ApplyTCPKeepAlive turns keepalive on and sets the idle time.
func ApplyTCPKeepAlive(c *net.TCPConn, idle, intvl time.Duration, cnt int) error {
	if c == nil {
		return errors.New("not a TCP connection")
	}
	if err := c.SetKeepAlive(true); err != nil {
		return err
	}
	if idle > 0 {
		return c.SetKeepAlivePeriod(idle)
	}
	return nil
}
