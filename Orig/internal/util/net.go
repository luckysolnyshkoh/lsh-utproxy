//go:build !linux

package util

import (
	"net"
)

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