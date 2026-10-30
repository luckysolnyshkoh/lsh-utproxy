//go:build !linux

package util

// RaiseFDLimit is a no-op outside Linux: there the process limit comes from
// ulimit/launchd. It returns (0, false).
func RaiseFDLimit(want int) (int, bool) {
	return 0, false
}
