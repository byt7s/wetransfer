//go:build !windows

// Package console prepares the Windows console for ANSI output and UTF-8.
// On other systems there is nothing to do.
package console

func Enable() {}
