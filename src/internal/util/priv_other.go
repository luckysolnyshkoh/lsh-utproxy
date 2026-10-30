//go:build !linux

package util

import "fmt"

// PrivilegesSupported reports that chroot/user/group cannot be dropped from
// inside the process. On Windows the service account is set with the Service
// Control Manager (sc.exe / services.msc), on macOS/Linux use a systemd unit or
// "sudo -u".
const PrivilegesSupported = false

// ApplyPrivileges returns an error if the config asked for privileges that this
// platform cannot drop.
func ApplyPrivileges(chrootDir, username, group string) error {
	if chrootDir == "" && username == "" && group == "" {
		return nil
	}
	return fmt.Errorf("chroot/user/group are only supported on Linux; run lsh-utproxy under a service account instead")
}
