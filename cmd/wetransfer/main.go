package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"wetransfer/internal/cli"
	"wetransfer/internal/console"
)

func main() {
	console.Enable()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(cli.Run(ctx, os.Args[1:]))
}
