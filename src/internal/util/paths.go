package util

import (
	"os"
	"path/filepath"
	"runtime"
)

// DefaultInstallDir is the packaged installation location:
// C:\Soft\lsh-utproxy on Windows, /opt/lsh-utproxy on Linux and other Unix.
func DefaultInstallDir() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(`C:\Soft`, "lsh-utproxy")
	}
	return "/opt/lsh-utproxy"
}

// DefaultConfigPath is where "-c" looks when no path is given.
func DefaultConfigPath() string {
	return filepath.Join(DefaultInstallDir(), "config", "lsh-utproxy.cfg")
}

// ResolveConfigPath returns the config file to read. An explicit -c path is
// used as given. Otherwise the installed default is preferred, and a checkout
// (./config/...) is used as a fallback so the program can be run without being
// installed.
func ResolveConfigPath(given string) string {
	if given != "" {
		return given
	}
	for _, c := range []string{
		DefaultConfigPath(),
		filepath.Join(".", "config", "lsh-utproxy.cfg"),
		filepath.Join(".", "config", "example.cfg"),
	} {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return DefaultConfigPath()
}
