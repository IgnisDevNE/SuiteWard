// Command suiteward runs the SuiteWard service and its diagnostic client.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/IgnisDevNE/SuiteWard/internal/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := app.Run(ctx, os.Args[1:], app.OSEnv(), os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
