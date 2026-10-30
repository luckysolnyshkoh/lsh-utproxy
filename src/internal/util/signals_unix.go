//go:build unix

package util

import (
	"os"
	"syscall"
)

// stopSignals: SIGINT (Ctrl+C), SIGTERM (systemctl stop) and SIGHUP
// (HAProxy treats it as a graceful stop too). SIGQUIT gives a core dump in Go,
// so it is not used for shutdown.
func stopSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
}
