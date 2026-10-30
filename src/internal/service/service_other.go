//go:build !windows

package service

import (
	"context"
	"errors"
)

// RunningUnderSCM is always false outside Windows: there is no Service Control
// Manager. On Linux the process is supervised by systemd.
func RunningUnderSCM() bool { return false }

// Run reports that service mode belongs to the Windows build.
func Run(name string, start func(context.Context) error) error {
	return errors.New("service mode (-service) exists only in the Windows build; on Linux use the systemd unit in deploy/")
}
