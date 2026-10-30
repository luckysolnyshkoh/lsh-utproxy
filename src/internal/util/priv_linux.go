//go:build linux

package util

import (
	"fmt"
	"os/user"
	"strconv"

	"golang.org/x/sys/unix"
)

// PrivilegesSupported reports that chroot/user/group can be applied.
const PrivilegesSupported = true

// ApplyPrivileges drops privileges after every listener is open, so binding
// ports below 1024 still works when the process starts as root.
//
// Order matters: users and groups are resolved *before* the chroot, because the
// password file of the real system is not reachable afterwards. Then chroot,
// then the group, then the user. Empty values are skipped.
//
// Note that a chroot also hides DNS configuration from the process: backend
// addresses must be given as IPs if you chroot.
func ApplyPrivileges(chrootDir, username, group string) error {
	if chrootDir == "" && username == "" && group == "" {
		return nil
	}
	if unix.Geteuid() != 0 {
		return fmt.Errorf("chroot/user/group require root, running as uid %d", unix.Geteuid())
	}

	gid := -1
	if group != "" {
		g, err := user.LookupGroup(group)
		if err != nil {
			return fmt.Errorf("group %s: %w", group, err)
		}
		v, err := strconv.Atoi(g.Gid)
		if err != nil {
			return fmt.Errorf("group %s: bad gid %q: %w", group, g.Gid, err)
		}
		gid = v
	}

	uid := -1
	if username != "" {
		u, err := user.Lookup(username)
		if err != nil {
			return fmt.Errorf("user %s: %w", username, err)
		}
		v, err := strconv.Atoi(u.Uid)
		if err != nil {
			return fmt.Errorf("user %s: bad uid %q: %w", username, u.Uid, err)
		}
		uid = v
		if gid < 0 {
			if g, err := strconv.Atoi(u.Gid); err == nil {
				gid = g
			}
		}
	}

	if chrootDir != "" {
		if err := unix.Chroot(chrootDir); err != nil {
			return fmt.Errorf("chroot %s: %w", chrootDir, err)
		}
		if err := unix.Chdir("/"); err != nil {
			return fmt.Errorf("chdir / after chroot: %w", err)
		}
	}

	if gid >= 0 {
		if err := unix.Setgid(gid); err != nil {
			return fmt.Errorf("setgid %d: %w", gid, err)
		}
	}
	if uid >= 0 {
		if err := unix.Setuid(uid); err != nil {
			return fmt.Errorf("setuid %d: %w", uid, err)
		}
	}
	return nil
}
