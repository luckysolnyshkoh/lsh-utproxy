//go:build windows

package util

import "os"

// stopSignals covers a console run (Ctrl+C / Ctrl+Break is os.Interrupt on
// Windows). SIGTERM is never delivered to Windows processes; a service is
// stopped through the Service Control Manager instead.
func stopSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
