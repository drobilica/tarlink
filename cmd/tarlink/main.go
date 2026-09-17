package main

import (
	"context"
	"fmt"
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
	if cli.MetaCommand(os.Args[1:]) {
		os.Exit(runner.Run(ctx, os.Args[1:]))
	}
	if err := app.CheckEnvironment(); err != nil {
		os.Exit(runner.Fail(err))
	}
	layout, err := filesystem.NewLayout()
	if err != nil {
		os.Exit(runner.Fail(err))
	}
	client := download.NewClient()
	client.SourceDiagnostic = func(message string) {
		_, _ = fmt.Fprintf(os.Stderr, "artifact source: %s\n", message)
	}
	// `run` is intentionally handled before the Cobra lifecycle: it resolves
	// only local state and replaces this process with the compiled closure.
	// It cannot refresh the registry, download, or select another runtime.
	if len(os.Args) >= 2 && os.Args[1] == "run" {
		if len(os.Args) < 3 || os.Args[2] == "" {
			os.Exit(runner.Fail(fmt.Errorf("usage: tarlink run <app-id> [-- <arguments...>]")))
		}
		core, err := app.NewCore(layout, client)
		if err != nil {
			os.Exit(runner.Fail(err))
		}
		arguments := append([]string(nil), os.Args[3:]...)
		if len(arguments) > 0 && arguments[0] == "--" {
			arguments = arguments[1:]
		}
		launch, err := core.PrepareRun(os.Args[2], arguments)
		if err != nil {
			os.Exit(runner.Fail(err))
		}
		if err := os.Chdir(launch.Dir); err != nil {
			os.Exit(runner.Fail(err))
		}
		if err := syscall.Exec(launch.Program, launch.Args, launch.Env); err != nil {
			os.Exit(runner.Fail(err))
		}
		return
	}
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
