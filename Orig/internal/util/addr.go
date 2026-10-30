package util

import (
	"errors"
	"strconv"
	"strings"
)

var ErrInvalidAddr = errors.New("invalid address")

func SplitHostPort(addr string) (host string, port int, err error) {
	if addr == "" {
		return "", 0, ErrInvalidAddr
	}
	if strings.HasPrefix(addr, "[") {
		rb := strings.Index(addr, "]")
		if rb < 0 {
			return "", 0, ErrInvalidAddr
		}
		host = addr[1:rb]
		if rb+1 < len(addr) && addr[rb+1] == ':' {
			pstr := addr[rb+2:]
			pv, e := strconv.Atoi(pstr)
			if e != nil {
				return host, 0, e
			}
			port = pv
		}
		return host, port, nil
	}
	last := strings.LastIndex(addr, ":")
	if last < 0 {
		return addr, 0, nil
	}
	host = addr[:last]
	pstr := addr[last+1:]
	pv, e := strconv.Atoi(pstr)
	if e != nil {
		return host, 0, e
	}
	port = pv
	return host, port, nil
}

func JoinHostPort(host string, port int) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + strconv.Itoa(port)
	}
	return host + ":" + strconv.Itoa(port)
}

func NormalizeBind(addr string) string {
	if addr == "" {
		return "0.0.0.0:0"
	}
	if addr == "*" {
		return "0.0.0.0:0"
	}
	if strings.HasPrefix(addr, "*:") {
		return "0.0.0.0:" + addr[2:]
	}
	if addr == "::" {
		return "[::]:0"
	}
	if strings.HasPrefix(addr, "::") && !strings.HasPrefix(addr, "[::]") {
		return "[::]:" + strings.TrimPrefix(addr, "::")
	}
	return addr
}