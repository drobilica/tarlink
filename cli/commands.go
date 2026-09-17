package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/drobilica/tarlink/internal/app"
	"github.com/drobilica/tarlink/internal/artifactrepo"
	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/research"
	"github.com/drobilica/tarlink/internal/version"
	"github.com/spf13/cobra"
)

const help = `TarLink manages verified portable Linux applications.

Usage:
  tarlink <command> [options]

Discover applications:
  list         List available applications
  installed    Write versioned installed-application JSON
  search       Search applications
  info         Show application information
  versions     Show application versions

Manage applications:
  install      Install one or more applications
  lock         Write an installed-state lock snapshot
  update       Update one or all installed applications
  rollback     Roll back an application
  uninstall    Uninstall one or more applications

Launch:
  run          Launch an installed application offline
  pin          Pin an installed application
  unpin        Unpin an installed application

Catalog:
  refresh      Refresh the application catalog

Maintenance:
  doctor       Audit TarLink-managed state
  self-update  Update TarLink itself
  version      Show TarLink version (-v, --version)
  completion   Generate a Bash, Zsh, Fish, or PowerShell completion script

Repositories:
  repository   Manage static artifact repositories

Registry development:
  registry     Registry validation and maintainer tools

Run 'tarlink <command> --help' for command-specific help.
`

const registryHelp = `Registry maintenance commands:
  tarlink registry validate <path>
  tarlink registry check <path> [--app <id> | --old-root <path> | --all-artifacts]
  tarlink registry freshness <app> [--json]
  tarlink registry inspect <owner/repo | release-asset-url | manifest.yaml | directory> [--json] [--refresh]
  tarlink registry add <owner/repo | release-asset-url> [--non-interactive] [--json] [--dry-run] [--output <path>]
  tarlink registry candidates [--changed] [--json] [--markdown]
  tarlink registry blockers [--capability <capability>] [--json]
  tarlink registry icons <path> [--app <id>] [--fix] [--json]
`

type commandFailure struct {
	code   int
	err    error
	silent bool
}

func (e *commandFailure) Error() string { return e.err.Error() }
func (e *commandFailure) Unwrap() error { return e.err }

func invalidCommand(usage string) error {
	return &commandFailure{code: exitInvalidArguments, err: errors.New(usage)}
}

func exactArgs(count int, usage string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) != count {
			return invalidCommand(usage)
		}
		for _, value := range args {
			if value == "" || strings.HasPrefix(value, "-") {
				return invalidCommand(usage)
			}
		}
		return nil
	}
}

func noArgs(usage string) cobra.PositionalArgs { return exactArgs(0, usage) }

func configureCommand(command *cobra.Command, usage string) {
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetFlagErrorFunc(func(_ *cobra.Command, _ error) error { return invalidCommand(usage) })
	command.SetHelpFunc(func(command *cobra.Command, _ []string) {
		_, _ = fmt.Fprintln(command.OutOrStdout(), usage)
	})
}

// RegistryMaintainerCommand reports whether arguments select a registry
// maintainer subcommand that must not construct the Linux application runtime.
func RegistryMaintainerCommand(arguments []string) bool {
	if len(arguments) < 2 || arguments[0] != "registry" {
		return false
	}
	switch arguments[1] {
	case "validate", "inspect", "add", "candidates", "blockers", "icons":
		return true
	default:
		return false
	}
}

// MetaCommand reports whether Cobra can serve the request without any runtime.
func MetaCommand(arguments []string) bool {
	if len(arguments) == 0 {
		return false
	}
	if arguments[0] == "help" || arguments[0] == "completion" {
		return true
	}
	if arguments[0] == "registry" && (len(arguments) == 1 || len(arguments) == 2 && arguments[1] == "help") {
		return true
	}
	for _, argument := range arguments {
		if argument == "--help" || argument == "-h" {
			return true
		}
	}
	return len(arguments) == 1 && (arguments[0] == "version" || arguments[0] == "-v" || arguments[0] == "--version")
}

func (r Runner) Run(ctx context.Context, arguments []string) int {
	if r.Stdout == nil {
		r.Stdout = io.Discard
	}
	if r.Stderr == nil {
		r.Stderr = io.Discard
	}
	if r.Stdin == nil {
		r.Stdin = strings.NewReader("")
	}
	tuiStdout, tuiStderr := r.Stdout, r.Stderr
	progress := r.progress()
	r.Stdout = progressOutput{Writer: r.Stdout, finish: progress.finish}
	r.Stderr = progressOutput{Writer: r.Stderr, finish: progress.finish}

	root := r.rootCommand(progress, tuiStdout, tuiStderr)
	root.SetArgs(append([]string{}, arguments...))
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	var failure *commandFailure
	if errors.As(err, &failure) {
		if !failure.silent {
			_, _ = fmt.Fprintln(r.Stderr, failure.err)
		}
		return failure.code
	}
	if strings.HasPrefix(err.Error(), "unknown command ") {
		return r.invalid("unknown command; run `tarlink help`")
	}
	return r.fail(err)
}

func (r Runner) rootCommand(progress *progressRenderer, tuiStdout, tuiStderr io.Writer) *cobra.Command {
	var versionFlag bool
	root := &cobra.Command{
		Use:           "tarlink",
		Short:         "Manage verified portable Linux applications",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(command *cobra.Command, args []string) error {
			if len(args) != 0 {
				return invalidCommand("unknown command; run `tarlink help`")
			}
			if versionFlag {
				_, err := fmt.Fprintf(r.Stdout, "tarlink %s\n", version.Current)
				return err
			}
			if r.LaunchTUI == nil {
				return errors.New("TUI is unavailable")
			}
			return r.LaunchTUI(command.Context(), r.Service, tuiStdout, tuiStderr)
		},
	}
	root.SetOut(r.Stdout)
	root.SetErr(r.Stderr)
	root.SetIn(r.Stdin)
	root.SetHelpFunc(func(command *cobra.Command, _ []string) { _, _ = io.WriteString(command.OutOrStdout(), help) })
	root.SetFlagErrorFunc(func(*cobra.Command, error) error { return invalidCommand("unknown command; run `tarlink help`") })
	root.Flags().BoolVarP(&versionFlag, "version", "v", false, "show TarLink version")
	root.CompletionOptions.DisableDefaultCmd = true

	root.AddCommand(
		r.versionCommand(), r.completionCommand(), r.refreshCommand(progress), r.searchCommand(), r.installCommand(progress),
		r.lockCommand(), r.updateCommand(progress), r.pinCommand(true), r.pinCommand(false),
		r.selfUpdateCommand(progress), r.doctorCommand(), r.listCommand(), r.installedCommand(),
		r.infoCommand(), r.versionsCommand(), r.rollbackCommand(progress), r.uninstallCommand(progress),
		r.registryCommand(), r.repositoryCommand(),
	)
	return root
}

func (r Runner) repositoryCommand() *cobra.Command {
	command := &cobra.Command{Use: "repository", Short: "Manage static artifact repositories"}
	command.AddCommand(r.repositoryInitCommand(), r.repositorySyncCommand(), r.repositoryVerifyCommand(), r.repositoryStatusCommand(), r.repositoryAddCommand(), r.repositoryRemoveCommand(), r.repositoryListCommand())
	return command
}

type repositoryOperations interface {
	RepositoryInit(string) error
	RepositoryVerify(context.Context, string) ([]artifactrepo.Object, error)
	RepositorySync(context.Context, string, artifactrepo.Selection, bool) (app.RepositoryReport, error)
	RepositoryStatus(context.Context, string, artifactrepo.Selection) (app.RepositoryReport, error)
}

func (r Runner) repositoryInitCommand() *cobra.Command {
	usage := "usage: tarlink repository init PATH"
	command := &cobra.Command{Use: "init PATH", Args: exactArgs(1, usage), RunE: func(_ *cobra.Command, args []string) error {
		service, err := r.requireService()
		if err != nil {
			return err
		}
		ops, ok := service.(repositoryOperations)
		if !ok {
			return errors.New("repository management is unavailable")
		}
		return ops.RepositoryInit(args[0])
	}}
	configureCommand(command, usage)
	return command
}
func (r Runner) repositoryVerifyCommand() *cobra.Command {
	usage := "usage: tarlink repository verify PATH"
	command := &cobra.Command{Use: "verify PATH", Args: exactArgs(1, usage), RunE: func(cmd *cobra.Command, args []string) error {
		service, err := r.requireService()
		if err != nil {
			return err
		}
		ops, ok := service.(repositoryOperations)
		if !ok {
			return errors.New("repository management is unavailable")
		}
		objects, err := ops.RepositoryVerify(cmd.Context(), args[0])
		if err == nil {
			_, err = fmt.Fprintf(r.Stdout, "Verified %d objects. Registry approval is not implied.\n", len(objects))
		}
		return err
	}}
	configureCommand(command, usage)
	return command
}
func (r Runner) repositorySyncCommand() *cobra.Command {
	var appID, platform string
	var all, dry bool
	usage := "usage: tarlink repository sync PATH [--app ID] [--platform PLATFORM] [--all-retained] [--dry-run]"
	command := &cobra.Command{Use: "sync PATH", Args: exactArgs(1, usage), PreRunE: func(_ *cobra.Command, _ []string) error {
		if (appID == "") == all {
			return invalidCommand(usage)
		}
		if appID != "" && platform != "" {
			if _, ok := manifest.ParsePlatformKey(platform); !ok {
				return invalidCommand(usage)
			}
		}
		return nil
	}, RunE: func(ctx *cobra.Command, args []string) error {
		service, err := r.requireService()
		if err != nil {
			return err
		}
		ops, ok := service.(repositoryOperations)
		if !ok {
			return errors.New("repository management is unavailable")
		}
		report, err := ops.RepositorySync(ctx.Context(), args[0], artifactrepo.Selection{App: appID, Platform: platform, AllRetained: all}, dry)
		if report.Revision != "" {
			if _, printErr := fmt.Fprintf(r.Stdout, "revision %s: required=%d present=%d missing=%d corrupt=%d\n", report.Revision, report.Required, report.Present, report.Missing, report.Corrupt); err == nil {
				err = printErr
			}
		}
		return err
	}}
	command.Flags().StringVar(&appID, "app", "", "exact application ID")
	command.Flags().StringVar(&platform, "platform", "", "linux-amd64 or linux-arm64")
	command.Flags().BoolVar(&all, "all-retained", false, "include all retained releases")
	command.Flags().BoolVar(&dry, "dry-run", false, "report without downloading or mutating")
	configureCommand(command, usage)
	return command
}
func (r Runner) repositoryStatusCommand() *cobra.Command {
	var appID, platform string
	var all bool
	usage := "usage: tarlink repository status PATH [--app ID] [--platform PLATFORM] [--all-retained]"
	command := &cobra.Command{Use: "status PATH", Args: exactArgs(1, usage), PreRunE: func(_ *cobra.Command, _ []string) error {
		if (appID == "") == all {
			return invalidCommand(usage)
		}
		return nil
	}, RunE: func(cmd *cobra.Command, args []string) error {
		service, err := r.requireService()
		if err != nil {
			return err
		}
		ops, ok := service.(repositoryOperations)
		if !ok {
			return errors.New("repository management is unavailable")
		}
		report, err := ops.RepositoryStatus(cmd.Context(), args[0], artifactrepo.Selection{App: appID, Platform: platform, AllRetained: all})
		if err == nil {
			_, err = fmt.Fprintf(r.Stdout, "revision %s: required=%d present=%d missing=%d corrupt=%d\n", report.Revision, report.Required, report.Present, report.Missing, report.Corrupt)
		}
		return err
	}}
	command.Flags().StringVar(&appID, "app", "", "exact application ID")
	command.Flags().StringVar(&platform, "platform", "", "linux-amd64 or linux-arm64")
	command.Flags().BoolVar(&all, "all-retained", false, "include all retained releases")
	configureCommand(command, usage)
	return command
}

func (r Runner) repositoryAddCommand() *cobra.Command {
	usage := "usage: tarlink repository add URL_OR_PATH"
	command := &cobra.Command{Use: "add URL_OR_PATH", Args: exactArgs(1, usage), RunE: func(_ *cobra.Command, args []string) error {
		service, err := r.requireService()
		if err != nil {
			return err
		}
		value, ok := service.(interface{ AddRepositorySource(string) error })
		if !ok {
			return errors.New("repository management is unavailable")
		}
		return value.AddRepositorySource(args[0])
	}}
	configureCommand(command, usage)
	return command
}

func (r Runner) repositoryRemoveCommand() *cobra.Command {
	usage := "usage: tarlink repository remove URL_OR_PATH"
	command := &cobra.Command{Use: "remove URL_OR_PATH", Args: exactArgs(1, usage), RunE: func(_ *cobra.Command, args []string) error {
		service, err := r.requireService()
		if err != nil {
			return err
		}
		value, ok := service.(interface{ RemoveRepositorySource(string) error })
		if !ok {
			return errors.New("repository management is unavailable")
		}
		return value.RemoveRepositorySource(args[0])
	}}
	configureCommand(command, usage)
	return command
}

func (r Runner) repositoryListCommand() *cobra.Command {
	usage := "usage: tarlink repository list"
	command := &cobra.Command{Use: "list", Args: noArgs(usage), RunE: func(_ *cobra.Command, _ []string) error {
		service, err := r.requireService()
		if err != nil {
			return err
		}
		value, ok := service.(interface{ RepositorySources() ([]string, error) })
		if !ok {
			return errors.New("repository management is unavailable")
		}
		sources, err := value.RepositorySources()
		if err != nil {
			return err
		}
		for _, source := range sources {
			if _, err := fmt.Fprintln(r.Stdout, source); err != nil {
				return err
			}
		}
		return nil
	}}
	configureCommand(command, usage)
	return command
}

func (r Runner) completionCommand() *cobra.Command {
	usage := "usage: tarlink completion <bash|zsh|fish|powershell>"
	command := &cobra.Command{
		Use:       "completion <bash|zsh|fish|powershell>",
		Short:     "Generate a shell completion script",
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return invalidCommand(usage)
			}
			switch args[0] {
			case "bash", "zsh", "fish", "powershell":
				return nil
			default:
				return invalidCommand(usage)
			}
		},
		RunE: func(command *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return command.Root().GenBashCompletion(r.Stdout)
			case "zsh":
				return command.Root().GenZshCompletion(r.Stdout)
			case "fish":
				return command.Root().GenFishCompletion(r.Stdout, true)
			default:
				return command.Root().GenPowerShellCompletion(r.Stdout)
			}
		},
	}
	configureCommand(command, usage)
	return command
}

func (r Runner) requireService() (app.Service, error) {
	if r.Service == nil {
		return nil, errors.New("TarLink core is unavailable")
	}
	return r.Service, nil
}

func (r Runner) versionCommand() *cobra.Command {
	usage := "usage: tarlink version"
	command := &cobra.Command{Use: "version", Short: "Show TarLink version", Args: noArgs(usage), RunE: func(*cobra.Command, []string) error {
		_, err := fmt.Fprintf(r.Stdout, "tarlink %s\n", version.Current)
		return err
	}}
	configureCommand(command, usage)
	return command
}

func (r Runner) refreshCommand(progress *progressRenderer) *cobra.Command {
	usage := "usage: tarlink refresh"
	command := &cobra.Command{Use: "refresh", Short: "Refresh the application catalog", Args: noArgs(usage), RunE: func(command *cobra.Command, _ []string) error {
		service, err := r.requireService()
		if err != nil {
			return err
		}
		checkedAt, err := service.SyncRegistry(command.Context(), progress.report)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(r.Stdout, "Application catalog refreshed. Checked at %s.\n", checkedAt.UTC().Format(time.RFC3339))
		return err
	}}
	configureCommand(command, usage)
	return command
}

func (r Runner) searchCommand() *cobra.Command {
	usage := "usage: tarlink search <query> [--json]"
	var jsonOutput bool
	command := &cobra.Command{Use: "search <query>", Short: "Search applications", Args: exactArgs(1, usage), RunE: func(command *cobra.Command, args []string) error {
		service, err := r.requireService()
		if err != nil {
			return err
		}
		result, err := service.Search(command.Context(), args[0])
		if err != nil {
			return err
		}
		return r.printApplications(result, jsonOutput, "No applications found.")
	}}
	command.Flags().BoolVar(&jsonOutput, "json", false, "write JSON")
	configureCommand(command, usage)
	return command
}

func (r Runner) installCommand(progress *progressRenderer) *cobra.Command {
	usage := "usage: tarlink install <app>... [--force-path] | tarlink install -f <path> [--force-path]"
	var file string
	var forcePath bool
	command := &cobra.Command{Use: "install <app>...", Short: "Install applications", Args: func(_ *cobra.Command, args []string) error {
		if (file == "") == (len(args) == 0) {
			return invalidCommand(usage)
		}
		for _, value := range args {
			if value == "" || strings.HasPrefix(value, "-") {
				return invalidCommand(usage)
			}
		}
		return nil
	}, RunE: func(command *cobra.Command, args []string) error {
		service, err := r.requireService()
		if err != nil {
			return err
		}
		if file != "" {
			lockService, ok := service.(app.LockService)
			if !ok {
				return errors.New("lockfile installation is unavailable")
			}
			result, err := lockService.InstallLock(command.Context(), file, forcePath, progress.report)
			if err != nil {
				return err
			}
			return r.printBatch("Installed", result)
		}
		if len(args) > 1 {
			batch, ok := service.(app.BatchInstallOptionsService)
			if !ok {
				return errors.New("batch installation is unavailable")
			}
			result, err := batch.InstallBatchWithOptions(command.Context(), args, forcePath, progress.report)
			if err != nil {
				return err
			}
			return r.printBatch("Installed", result)
		}
		selector, err := app.ParseSelector(args[0])
		if err != nil {
			return invalidCommand(usage)
		}
		conflicts, err := service.CheckInstallPath(selector.App)
		if err != nil {
			return err
		}
		if len(conflicts) != 0 && !forcePath {
			if err := r.printPathConflicts(args[0], conflicts); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(r.Stderr, "Refusing to install %s because a PATH conflict was detected.\nRe-run with --force-path to acknowledge and install anyway.\n", args[0])
			return &commandFailure{code: exitConflict, err: errors.New("PATH conflict"), silent: true}
		}
		result, err := service.Install(command.Context(), args[0], progress.report)
		if err != nil {
			return err
		}
		return r.printResult("Installed", result)
	}}
	command.Flags().StringVarP(&file, "file", "f", "", "install from lock snapshot")
	command.Flags().BoolVar(&forcePath, "force-path", false, "acknowledge PATH conflicts")
	configureCommand(command, usage)
	return command
}

func (r Runner) lockCommand() *cobra.Command {
	usage := "usage: tarlink lock [--output <path>]"
	var output string
	command := &cobra.Command{Use: "lock", Short: "Write an installed-state lock snapshot", Args: noArgs(usage), RunE: func(command *cobra.Command, _ []string) error {
		service, err := r.requireService()
		if err != nil {
			return err
		}
		lockService, ok := service.(app.LockService)
		if !ok {
			return errors.New("lockfile snapshots are unavailable")
		}
		if err := lockService.WriteLock(command.Context(), output); err != nil {
			return err
		}
		if output == "" {
			output = "tarlink.lock"
		}
		_, err = fmt.Fprintf(r.Stdout, "Wrote lock snapshot to %s\n", output)
		return err
	}}
	command.Flags().StringVar(&output, "output", "", "output path")
	configureCommand(command, usage)
	return command
}

func (r Runner) updateCommand(progress *progressRenderer) *cobra.Command {
	usage := "usage: tarlink update <app> | tarlink update --all"
	var all bool
	command := &cobra.Command{Use: "update <app>", Short: "Update applications", Args: func(_ *cobra.Command, args []string) error {
		if all {
			if len(args) != 0 {
				return invalidCommand(usage)
			}
			return nil
		}
		return exactArgs(1, usage)(nil, args)
	}, RunE: func(command *cobra.Command, args []string) error {
		service, err := r.requireService()
		if err != nil {
			return err
		}
		if all {
			result, err := service.UpdateAll(command.Context(), progress.report)
			if err != nil {
				return err
			}
			return r.printUpdateAll(result)
		}
		result, err := service.Update(command.Context(), args[0], progress.report)
		if err != nil {
			return err
		}
		return r.printResult("Updated", result)
	}}
	command.Flags().BoolVar(&all, "all", false, "update all installed applications")
	configureCommand(command, usage)
	return command
}

func (r Runner) pinCommand(pin bool) *cobra.Command {
	name := "unpin"
	label := "Unpin"
	if pin {
		name = "pin"
		label = "Pin"
	}
	usage := "usage: tarlink " + name + " <app>"
	command := &cobra.Command{Use: name + " <app>", Short: label + " an installed application", Args: exactArgs(1, usage), RunE: func(command *cobra.Command, args []string) error {
		service, err := r.requireService()
		if err != nil {
			return err
		}
		pins, ok := service.(interface {
			Pin(context.Context, string) error
			Unpin(context.Context, string) error
		})
		if !ok {
			return errors.New("pinning is unavailable")
		}
		if pin {
			err = pins.Pin(command.Context(), args[0])
		} else {
			err = pins.Unpin(command.Context(), args[0])
		}
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(r.Stdout, "%s %s\n", label, args[0])
		return err
	}}
	configureCommand(command, usage)
	return command
}

func (r Runner) selfUpdateCommand(progress *progressRenderer) *cobra.Command {
	usage := "usage: tarlink self-update"
	command := &cobra.Command{Use: "self-update", Short: "Update TarLink itself", Args: noArgs(usage), RunE: func(command *cobra.Command, _ []string) error {
		service, err := r.requireService()
		if err != nil {
			return err
		}
		value, err := service.UpgradeTarLink(command.Context(), progress.report)
		if err != nil {
			return err
		}
		if !value.UpgradeAvailable {
			_, err = fmt.Fprintf(r.Stdout, "TarLink %s is already up to date.\n", value.Current)
		} else {
			_, err = fmt.Fprintf(r.Stdout, "TarLink %s → %s\nTarLink upgraded to %s\n", value.Current, value.Latest, value.Latest)
		}
		return err
	}}
	configureCommand(command, usage)
	return command
}

func (r Runner) doctorCommand() *cobra.Command {
	usage := "usage: tarlink doctor"
	command := &cobra.Command{Use: "doctor", Short: "Audit TarLink-managed state", Args: noArgs(usage), RunE: func(command *cobra.Command, _ []string) error {
		service, err := r.requireService()
		if err != nil {
			return err
		}
		report, err := service.Doctor(command.Context())
		if err != nil {
			return err
		}
		if err := r.printDoctor(report); err != nil {
			return err
		}
		if report.Errors > 0 {
			return &app.Error{Code: app.CodeStateCorrupt, Op: "doctor", Err: errors.New("integrity errors found")}
		}
		return nil
	}}
	configureCommand(command, usage)
	return command
}

func (r Runner) listCommand() *cobra.Command {
	usage := "usage: tarlink list [--installed|--updates] [--json]"
	var installedOnly, updatesOnly, jsonOutput bool
	command := &cobra.Command{Use: "list", Short: "List available applications", Args: noArgs(usage), PreRunE: func(*cobra.Command, []string) error {
		if installedOnly && updatesOnly {
			return invalidCommand(usage)
		}
		return nil
	}, RunE: func(command *cobra.Command, _ []string) error {
		service, err := r.requireService()
		if err != nil {
			return err
		}
		result, err := service.ListAvailable(command.Context())
		if err != nil {
			return err
		}
		if installedOnly || updatesOnly {
			filtered := make([]app.Application, 0, len(result))
			for _, value := range result {
				if value.InstalledVersion != "" && (!updatesOnly || value.UpdateAvailable) {
					filtered = append(filtered, value)
				}
			}
			result = filtered
		}
		empty := "No applications found."
		if installedOnly {
			empty = "No installed applications."
		} else if updatesOnly {
			empty = "No updates available."
		}
		return r.printApplications(result, jsonOutput, empty)
	}}
	command.Flags().BoolVar(&installedOnly, "installed", false, "show installed applications")
	command.Flags().BoolVar(&updatesOnly, "updates", false, "show available updates")
	command.Flags().BoolVar(&jsonOutput, "json", false, "write JSON")
	configureCommand(command, usage)
	return command
}

type installedContract struct {
	Version      int                    `json:"version"`
	Applications []installedApplication `json:"applications"`
}

type installedApplication struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

func (r Runner) installedCommand() *cobra.Command {
	usage := "usage: tarlink installed --json"
	var jsonOutput bool
	command := &cobra.Command{Use: "installed", Short: "Write versioned installed-application JSON", Args: noArgs(usage), PreRunE: func(*cobra.Command, []string) error {
		if !jsonOutput {
			return invalidCommand(usage)
		}
		return nil
	}, RunE: func(command *cobra.Command, _ []string) error {
		service, err := r.requireService()
		if err != nil {
			return err
		}
		values, err := service.List(command.Context())
		if err != nil {
			return err
		}
		applications := make([]installedApplication, 0, len(values))
		for _, value := range values {
			if value.InstalledVersion != "" {
				applications = append(applications, installedApplication{ID: value.ID, Version: value.InstalledVersion})
			}
		}
		sort.Slice(applications, func(i, j int) bool { return applications[i].ID < applications[j].ID })
		return writeJSON(r.Stdout, installedContract{Version: 1, Applications: applications})
	}}
	command.Flags().BoolVar(&jsonOutput, "json", false, "write the versioned machine contract")
	configureCommand(command, usage)
	return command
}

func (r Runner) infoCommand() *cobra.Command {
	usage := "usage: tarlink info <app> [--json]"
	var jsonOutput bool
	command := &cobra.Command{Use: "info <app>", Short: "Show application information", Args: exactArgs(1, usage), RunE: func(command *cobra.Command, args []string) error {
		service, err := r.requireService()
		if err != nil {
			return err
		}
		value, err := service.Info(command.Context(), args[0])
		if err != nil {
			return err
		}
		return r.printInfo(value, jsonOutput)
	}}
	command.Flags().BoolVar(&jsonOutput, "json", false, "write JSON")
	configureCommand(command, usage)
	return command
}

func (r Runner) versionsCommand() *cobra.Command {
	usage := "usage: tarlink versions <app> [--json]"
	var jsonOutput bool
	command := &cobra.Command{Use: "versions <app>", Short: "Show application versions", Args: exactArgs(1, usage), RunE: func(command *cobra.Command, args []string) error {
		service, err := r.requireService()
		if err != nil {
			return err
		}
		values, err := service.Versions(command.Context(), args[0])
		if err != nil {
			return err
		}
		return r.printVersions(args[0], values, jsonOutput)
	}}
	command.Flags().BoolVar(&jsonOutput, "json", false, "write JSON")
	configureCommand(command, usage)
	return command
}

func (r Runner) rollbackCommand(progress *progressRenderer) *cobra.Command {
	usage := "usage: tarlink rollback <app>"
	command := &cobra.Command{Use: "rollback <app>", Short: "Roll back an application", Args: exactArgs(1, usage), RunE: func(command *cobra.Command, args []string) error {
		service, err := r.requireService()
		if err != nil {
			return err
		}
		result, err := service.Rollback(command.Context(), args[0], progress.report)
		if err != nil {
			return err
		}
		return r.printResult("Rolled back", result)
	}}
	configureCommand(command, usage)
	return command
}

func (r Runner) uninstallCommand(progress *progressRenderer) *cobra.Command {
	usage := "usage: tarlink uninstall <app>... | tarlink uninstall --all"
	var all bool
	command := &cobra.Command{Use: "uninstall <app>...", Short: "Uninstall applications", Args: func(_ *cobra.Command, args []string) error {
		if all {
			if len(args) != 0 {
				return invalidCommand(usage)
			}
			return nil
		}
		if len(args) == 0 {
			return invalidCommand(usage)
		}
		for _, value := range args {
			if value == "" || strings.HasPrefix(value, "-") {
				return invalidCommand(usage)
			}
		}
		return nil
	}, RunE: func(command *cobra.Command, args []string) error {
		service, err := r.requireService()
		if err != nil {
			return err
		}
		if all {
			result, runErr := service.UninstallAll(command.Context(), progress.report)
			if printErr := r.printUninstallAll(result); printErr != nil {
				return printErr
			}
			return runErr
		}
		if len(args) > 1 {
			batch, ok := service.(app.BatchService)
			if !ok {
				return errors.New("batch uninstallation is unavailable")
			}
			result, err := batch.UninstallBatch(command.Context(), args, progress.report)
			if err != nil {
				return err
			}
			return r.printBatch("Uninstalled", result)
		}
		result, err := service.Uninstall(command.Context(), args[0], progress.report)
		if err != nil {
			return err
		}
		return r.printResult("Uninstalled", result)
	}}
	command.Flags().BoolVar(&all, "all", false, "uninstall all applications")
	configureCommand(command, usage)
	return command
}

func (r Runner) registryCommand() *cobra.Command {
	command := &cobra.Command{Use: "registry", Short: "Registry validation and maintainer tools", RunE: func(command *cobra.Command, args []string) error {
		if len(args) == 1 && args[0] == "help" {
			_, err := io.WriteString(r.Stdout, registryHelp)
			return err
		}
		if len(args) != 0 {
			return invalidCommand("usage: tarlink registry <validate|check|freshness|inspect|add|candidates|blockers|icons> ...")
		}
		_, err := io.WriteString(r.Stdout, registryHelp)
		return err
	}}
	command.SetFlagErrorFunc(func(*cobra.Command, error) error {
		return invalidCommand("usage: tarlink registry <validate|check|freshness|inspect|add|candidates|blockers|icons> ...")
	})
	command.SetHelpFunc(func(command *cobra.Command, _ []string) { _, _ = io.WriteString(command.OutOrStdout(), registryHelp) })
	command.AddCommand(r.registryValidateCommand(), r.registryCheckCommand(), r.registryFreshnessCommand(), r.registryInspectCommand(), r.registryAddCommand(), r.registryCandidatesCommand(), r.registryBlockersCommand(), r.registryIconsCommand())
	return command
}

func (r Runner) registryValidateCommand() *cobra.Command {
	usage := "usage: tarlink registry validate <path>"
	command := &cobra.Command{Use: "validate <path>", Args: exactArgs(1, usage), RunE: func(command *cobra.Command, args []string) error {
		if r.Registry.Validation == nil {
			return errors.New("registry validation is unavailable")
		}
		if err := r.Registry.Validation.ValidateRegistry(command.Context(), args[0]); err != nil {
			return err
		}
		_, err := fmt.Fprintln(r.Stdout, "Registry is valid")
		return err
	}}
	configureCommand(command, usage)
	return command
}

func (r Runner) registryCheckCommand() *cobra.Command {
	usage := "usage: tarlink registry check <path> [--app <id> | --old-root <path> | --all-artifacts]"
	var options app.RegistryCheckOptions
	command := &cobra.Command{Use: "check <path>", Args: exactArgs(1, usage), PreRunE: func(*cobra.Command, []string) error {
		selected := 0
		if options.App != "" {
			selected++
		}
		if options.OldRoot != "" {
			selected++
		}
		if options.AllArtifacts {
			selected++
		}
		if selected > 1 {
			return invalidCommand(usage)
		}
		return nil
	}, RunE: func(command *cobra.Command, args []string) error {
		service, err := r.requireService()
		if err != nil {
			return err
		}
		checker, ok := service.(app.RegistryCheckService)
		if !ok {
			return errors.New("registry checker is unavailable")
		}
		options.Root = args[0]
		result, err := checker.CheckRegistry(command.Context(), options)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(r.Stdout, "Registry is valid; materialized %d artifact(s)\n", result.Materialized)
		return err
	}}
	command.Flags().StringVar(&options.App, "app", "", "check one application")
	command.Flags().StringVar(&options.OldRoot, "old-root", "", "previous registry tree")
	command.Flags().BoolVar(&options.AllArtifacts, "all-artifacts", false, "materialize all artifacts")
	configureCommand(command, usage)
	return command
}

func (r Runner) registryFreshnessCommand() *cobra.Command {
	usage := "usage: tarlink registry freshness <app> [--json]"
	var jsonOutput bool
	command := &cobra.Command{Use: "freshness <app>", Args: exactArgs(1, usage), RunE: func(command *cobra.Command, args []string) error {
		service, err := r.requireService()
		if err != nil {
			return err
		}
		fresh, ok := service.(app.FreshnessService)
		if !ok {
			return errors.New("registry freshness is unavailable")
		}
		report, err := fresh.Freshness(command.Context(), args[0])
		if err != nil {
			return err
		}
		return r.printFreshness(report, jsonOutput)
	}}
	command.Flags().BoolVar(&jsonOutput, "json", false, "write JSON")
	configureCommand(command, usage)
	return command
}

func (r Runner) registryInspectCommand() *cobra.Command {
	usage := "usage: tarlink registry inspect <owner/repo | release-asset-url | manifest.yaml | directory> [--json] [--refresh]"
	var jsonOutput, refresh bool
	var release, asset string
	command := &cobra.Command{Use: "inspect <target>", Args: exactArgs(1, usage), RunE: func(command *cobra.Command, args []string) error {
		target := args[0]
		_, directErr := research.ParseReleaseAssetURL(target)
		_, localErr := os.Lstat(target)
		if directErr == nil || localErr == nil {
			if release != "" || asset != "" {
				return invalidCommand(usage)
			}
			if r.Registry.Onboarding == nil {
				return errors.New("registry onboarding is unavailable")
			}
			value, err := r.Registry.Onboarding.InspectRegistry(command.Context(), app.RegistryInspectOptions{Target: target, Refresh: refresh})
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(r.Stdout, value)
			}
			return r.printRegistryInspection(value)
		}
		if r.Registry.Research == nil {
			return errors.New("registry research is unavailable")
		}
		opts := app.ResearchOptions{Repository: target, Release: release, Asset: asset, Refresh: refresh, Inspect: true}
		value, err := r.Registry.Research.Research(command.Context(), opts)
		if err != nil {
			if !jsonOutput {
				return err
			}
			if value.Status == "ERROR" && value.Error != nil {
				_ = writeJSON(r.Stdout, value)
				return &commandFailure{code: exitCode(err), err: err, silent: true}
			}
			code := string(app.CodeOf(err))
			errorValue := map[string]any{"message": err.Error(), "reason_code": code}
			var failure *app.ResearchFailure
			if errors.As(err, &failure) {
				code = failure.ReasonCode
				errorValue["reason_code"] = code
				if failure.Kind != "" {
					errorValue["kind"] = failure.Kind
				}
				if failure.HTTPStatus != 0 {
					errorValue["http_status"] = failure.HTTPStatus
				}
			}
			_ = writeJSON(r.Stdout, map[string]any{"error": errorValue})
			return &commandFailure{code: exitCode(err), err: err, silent: true}
		}
		return r.printResearch(value, jsonOutput)
	}}
	command.Flags().BoolVar(&jsonOutput, "json", false, "write JSON")
	command.Flags().BoolVar(&refresh, "refresh", false, "refresh cached research")
	command.Flags().StringVar(&release, "release", "", "select a release")
	command.Flags().StringVar(&asset, "asset", "", "select an asset")
	configureCommand(command, usage)
	return command
}

func (r Runner) registryAddCommand() *cobra.Command {
	usage := "usage: tarlink registry add <owner/repo | release-asset-url> [--non-interactive] [--json] [--dry-run] [--output <path>]"
	var options app.RegistryAddOptions
	var jsonOutput, dryRun, createBinLink, noCreateBinLink bool
	var output, categories string
	command := &cobra.Command{Use: "add <owner/repo | release-asset-url>", Args: exactArgs(1, usage), PreRunE: func(*cobra.Command, []string) error {
		if jsonOutput && !options.NonInteractive || dryRun && output != "" || createBinLink && noCreateBinLink {
			return invalidCommand(usage)
		}
		return nil
	}, RunE: func(command *cobra.Command, args []string) error {
		if r.Registry.Onboarding == nil {
			return errors.New("registry onboarding is unavailable")
		}
		options.Target = args[0]
		options.Categories = splitCSV(categories)
		if createBinLink {
			value := true
			options.CreateBinLink = &value
		} else if noCreateBinLink {
			value := false
			options.CreateBinLink = &value
		}
		value, err := r.Registry.Onboarding.AddRegistry(command.Context(), options)
		if err != nil {
			return err
		}
		if !options.NonInteractive && value.Status == "needs-input" {
			if err = r.printCandidate(value.Candidate); err != nil {
				return err
			}
			options, value.Candidate, err = r.promptRegistryAdd(options, value.Candidate, value.Required)
			if err != nil {
				return err
			}
			value, err = app.CompleteRegistryCandidate(value.Candidate, options)
			if err != nil {
				return err
			}
		}
		if jsonOutput {
			if err := writeJSON(r.Stdout, value); err != nil {
				return err
			}
			if value.Status == "needs-input" {
				return &commandFailure{code: 2, err: errors.New("registry add needs input"), silent: true}
			}
			return nil
		}
		if value.Status == "needs-input" {
			return fmt.Errorf("registry add needs input: %s", requiredFields(value.Required))
		}
		if dryRun {
			_, err = fmt.Fprintf(r.Stdout, "Candidate manifest (dry run):\n%s", value.YAML)
			return err
		}
		if output != "" {
			if err := writeNewFile(output, value.YAML); err != nil {
				return err
			}
			_, err = fmt.Fprintf(r.Stdout, "Wrote candidate manifest to %s\n", output)
			return err
		}
		_, err = r.Stdout.Write(value.YAML)
		return err
	}}
	command.Flags().BoolVar(&options.NonInteractive, "non-interactive", false, "do not prompt")
	command.Flags().BoolVar(&jsonOutput, "json", false, "write JSON")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "print candidate YAML")
	command.Flags().BoolVar(&options.Refresh, "refresh", false, "refresh cached research")
	command.Flags().StringVar(&options.ID, "id", "", "application ID")
	command.Flags().StringVar(&options.Name, "name", "", "application name")
	command.Flags().StringVar(&options.Summary, "summary", "", "application summary")
	command.Flags().StringVar(&categories, "categories", "", "comma-separated categories")
	command.Flags().StringVar(&output, "output", "", "output path")
	command.Flags().BoolVar(&createBinLink, "create-bin-link", false, "create a command link")
	command.Flags().BoolVar(&noCreateBinLink, "no-create-bin-link", false, "do not create a command link")
	configureCommand(command, usage)
	return command
}

func (r Runner) registryCandidatesCommand() *cobra.Command {
	usage := "usage: tarlink registry candidates [--changed] [--json] [--markdown]"
	var changed, jsonOutput, markdown bool
	command := &cobra.Command{Use: "candidates", Args: noArgs(usage), RunE: func(command *cobra.Command, _ []string) error {
		if r.Registry.Candidates == nil {
			return errors.New("candidate ledger is unavailable")
		}
		if jsonOutput && markdown {
			return invalidCommand(usage + ": --json and --markdown are mutually exclusive")
		}
		if changed && markdown {
			return invalidCommand(usage + ": --changed and --markdown are mutually exclusive")
		}
		if changed {
			value, err := r.Registry.Candidates.CandidateChanges(command.Context())
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(r.Stdout, value)
			}
			return printChanges(r.Stdout, value)
		}
		value, err := r.Registry.Candidates.CandidateLedger()
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSON(r.Stdout, value)
		}
		if markdown {
			_, err := io.WriteString(r.Stdout, research.RenderCandidateReport(value))
			return err
		}
		for _, candidate := range value.Candidates {
			if _, err := fmt.Fprintf(r.Stdout, "%-24s %-10s %s\n", candidate.ID, candidate.Status, candidate.Upstream); err != nil {
				return err
			}
		}
		return nil
	}}
	command.Flags().BoolVar(&changed, "changed", false, "show changed candidates")
	command.Flags().BoolVar(&jsonOutput, "json", false, "write JSON")
	command.Flags().BoolVar(&markdown, "markdown", false, "write Markdown research report")
	configureCommand(command, usage)
	return command
}

func (r Runner) registryBlockersCommand() *cobra.Command {
	usage := "usage: tarlink registry blockers [--capability <capability>] [--json]"
	var capability string
	var jsonOutput bool
	command := &cobra.Command{Use: "blockers", Args: noArgs(usage), RunE: func(*cobra.Command, []string) error {
		if r.Registry.Blockers == nil {
			return errors.New("blocker analysis is unavailable")
		}
		if capability == "" {
			values, err := r.Registry.Blockers.Blockers("")
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(r.Stdout, values)
			}
			for _, value := range values {
				if _, err := fmt.Fprintf(r.Stdout, "%-32s %d\n", value.Blocker, value.Count); err != nil {
					return err
				}
			}
			return nil
		}
		values, err := r.Registry.Blockers.CapabilityPreflight(capability)
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSON(r.Stdout, values)
		}
		return printCapability(r.Stdout, values)
	}}
	command.Flags().StringVar(&capability, "capability", "", "analyze a capability")
	command.Flags().BoolVar(&jsonOutput, "json", false, "write JSON")
	configureCommand(command, usage)
	return command
}

func (r Runner) registryIconsCommand() *cobra.Command {
	usage := "usage: tarlink registry icons <path> [--app <id>] [--fix] [--json]"
	var options app.RegistryIconOptions
	var jsonOutput bool
	command := &cobra.Command{Use: "icons <path>", Args: exactArgs(1, usage), RunE: func(command *cobra.Command, args []string) error {
		if r.Registry.Icons == nil {
			return errors.New("registry icon maintenance is unavailable")
		}
		options.Root = args[0]
		report, err := r.Registry.Icons.RegistryIcons(command.Context(), options)
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSON(r.Stdout, report)
		}
		for _, result := range report.Results {
			if _, err := fmt.Fprintf(r.Stdout, "%-24s %-8s %s\n", result.App, result.Status, result.Error); err != nil {
				return err
			}
		}
		return nil
	}}
	command.Flags().StringVar(&options.App, "app", "", "select an application")
	command.Flags().BoolVar(&options.Fix, "fix", false, "repair icon metadata")
	command.Flags().BoolVar(&jsonOutput, "json", false, "write JSON")
	configureCommand(command, usage)
	return command
}
