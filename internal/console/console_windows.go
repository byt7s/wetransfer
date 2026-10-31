//go:build windows

// Package console prepares the Windows console for ANSI output and UTF-8.
package console

import (
	"os"

	"golang.org/x/sys/windows"
)

func Enable() {
	var mode uint32
	for _, f := range []*os.File{os.Stdout, os.Stderr, os.Stdin} {
		h := windows.Handle(f.Fd())
		if windows.GetConsoleMode(h, &mode) == nil {
			_ = windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
		}
	}
	_ = windows.SetConsoleOutputCP(65001)
	_ = windows.SetConsoleCP(65001)
}
