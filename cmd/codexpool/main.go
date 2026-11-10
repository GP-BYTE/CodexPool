package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"codexpool/internal/cli"
	"codexpool/internal/textsafe"
)

func main() {
	if code, handled := cli.RunStatic(os.Args[1:], os.Stdout); handled {
		os.Exit(code)
	}
	app, err := cli.NewDefault()
	if err != nil {
		fmt.Fprintln(os.Stderr, "codexpool:", textsafe.Clean(err.Error(), 800))
		os.Exit(cli.ExitRuntimeFailure)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	code := app.Run(ctx, os.Args[1:])
	if code < 0 {
		code = cli.ExitRuntimeFailure
	}
	if code > 255 {
		code = 255
	}
	os.Exit(code)
}
