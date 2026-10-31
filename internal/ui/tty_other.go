//go:build !darwin && !linux

package ui

import "os"

// Best effort on platforms without a termios ioctl: a character device is
// usually a terminal, though /dev/null also matches.
func isatty(fd uintptr) bool {
	f := os.NewFile(fd, "fd")
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
