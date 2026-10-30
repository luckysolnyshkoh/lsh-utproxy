//go:build !linux

package util

import (
	"net"
)

// ReusePortSupported reports whether SO_REUSEPORT is available on this
// platform. HAProxy-style "re-use-port" is downgraded to a normal listener
// when it is false, and the proxy logs a warning once.
const ReusePortSupported = false

func ListenUDPReuse(addr string) (*net.UDPConn, error) {
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	return net.ListenUDP("udp", ua)
}

func ListenTCPReuse(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}
