//go:build windows

// Package console prepares the Windows console for ANSI output and UTF-8.
package console

import (
	"os"

	"golang.org/x/sys/windows"
)

func Enable() {
	out := windows.Handle(os.Stdout.Fd())
	var mode uint32
	if windows.GetConsoleMode(out, &mode) == nil {
		_ = windows.SetConsoleMode(out, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
	}
	in := windows.Handle(os.Stdin.Fd())
	if windows.GetConsoleMode(in, &mode) == nil {
		_ = windows.SetConsoleMode(in, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
	}
	_ = windows.SetConsoleOutputCP(65001)
	_ = windows.SetConsoleCP(65001)
}
