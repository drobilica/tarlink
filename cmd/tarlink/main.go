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

	runner := cli.Runner{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}
	if err := app.CheckEnvironment(); err != nil {
		os.Exit(runner.Fail(err))
	}
	if len(os.Args) == 2 && (os.Args[1] == "version" || os.Args[1] == "help" || os.Args[1] == "--help" || os.Args[1] == "-h") {
		os.Exit(runner.Run(ctx, os.Args[1:]))
	}
	layout, err := filesystem.NewLayout()
	if err != nil {
		os.Exit(runner.Fail(err))
	}
	client := download.NewClient()
	if cli.RegistryMaintainerCommand(os.Args[1:]) {
		maintainer := app.NewMaintainer(layout, client)
		runner.Registry = cli.RegistryTools{
			Validation: maintainer,
			Research:   maintainer,
			Onboarding: maintainer,
			Candidates: maintainer,
			Blockers:   maintainer,
			Icons:      maintainer,
		}
	} else {
		core, err := app.NewCore(layout, client)
		if err != nil {
			os.Exit(runner.Fail(err))
		}
		runner.Service = core
	}
	runner.LaunchTUI = func(ctx context.Context, service app.Service, stdout, _ io.Writer) error {
		return tui.Run(ctx, service, os.Stdin, stdout)
	}
	os.Exit(runner.Run(ctx, os.Args[1:]))
}
