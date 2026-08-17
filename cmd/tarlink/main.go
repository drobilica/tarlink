package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/drobilica/tarlink/cli"
	"github.com/drobilica/tarlink/internal/app"
	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/tui"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runner := cli.Runner{Stdout: os.Stdout, Stderr: os.Stderr}
	if err := app.CheckEnvironment(); err != nil {
		os.Exit(runner.Fail(err))
	}
	if len(os.Args) == 1 || len(os.Args) == 2 && (os.Args[1] == "version" || os.Args[1] == "help" || os.Args[1] == "--help" || os.Args[1] == "-h") {
		os.Exit(runner.Run(ctx, os.Args[1:]))
	}
	layout, err := filesystem.NewLayout()
	if err != nil {
		os.Exit(runner.Fail(err))
	}
	service, err := app.NewCore(layout, download.NewClient())
	if err != nil {
		os.Exit(runner.Fail(err))
	}
	runner.Service = service
	runner.LaunchTUI = func(ctx context.Context, service app.Service, stdout, _ io.Writer) error {
		return tui.Run(ctx, service, os.Stdin, stdout)
	}
	os.Exit(runner.Run(ctx, os.Args[1:]))
}
