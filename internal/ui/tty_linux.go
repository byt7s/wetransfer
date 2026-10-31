//go:build linux

package ui

import (
	"syscall"
	"unsafe"
)

// TCGETS on Linux.
const ioctlReadTermios = 0x5401

func isatty(fd uintptr) bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, fd, ioctlReadTermios, uintptr(unsafe.Pointer(&t)), 0, 0, 0)
	return errno == 0
}
