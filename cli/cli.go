// Package cli is the thin, presentation-only TarLink command interface.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/drobilica/tarlink/internal/app"
	"github.com/drobilica/tarlink/internal/freshness"
	"github.com/drobilica/tarlink/internal/research"
	"github.com/drobilica/tarlink/internal/version"
)

const help = `TarLink manages verified portable Linux applications.

Usage:
  tarlink <command> [options]

Discover applications:
  list         List available applications
  search       Search applications
  info         Show application information
  versions     Show application versions

Manage applications:
  install      Install one or more applications
  lock         Write an installed-state lock snapshot
  update       Update one or all installed applications
  rollback     Roll back an application
  uninstall    Uninstall one or more applications
  pin          Pin an installed application
  unpin        Unpin an installed application

Catalog:
  refresh      Refresh the application catalog

Maintenance:
  doctor       Audit TarLink-managed state
  self-update  Update TarLink itself
  version      Show TarLink version

Registry development:
  registry     Registry validation and maintainer tools

Run 'tarlink <command> --help' for command-specific help.
`

const registryHelp = `Registry maintenance commands:
  tarlink registry validate <path>
  tarlink registry check <path> [--app <id> | --old-root <path> | --all-artifacts]
  tarlink registry freshness <app> [--json]
  tarlink registry inspect <owner/repo | release-asset-url | manifest.yaml | directory> [--json] [--refresh]
  tarlink registry add <release-asset-url> [--non-interactive] [--json] [--dry-run] [--output <path>]
  tarlink registry candidates [--changed] [--json]
  tarlink registry blockers [--capability <capability>] [--json]
  tarlink registry icons <path> [--app <id>] [--fix] [--json]
`

var commandHelp = map[string]string{
	"refresh":     "usage: tarlink refresh",
	"list":        "usage: tarlink list [--installed|--updates] [--json]",
	"search":      "usage: tarlink search <query> [--json]",
	"install":     "usage: tarlink install <app>... [--force-path] | tarlink install -f <path> [--force-path]",
	"lock":        "usage: tarlink lock [--output <path>]",
	"update":      "usage: tarlink update <app> | tarlink update --all",
	"self-update": "usage: tarlink self-update",
	"doctor":      "usage: tarlink doctor",
	"version":     "usage: tarlink version",
	"pin":         "usage: tarlink pin <app>",
	"unpin":       "usage: tarlink unpin <app>",
	"info":        "usage: tarlink info <app> [--json]",
	"versions":    "usage: tarlink versions <app> [--json]",
	"rollback":    "usage: tarlink rollback <app>",
	"uninstall":   "usage: tarlink uninstall <app>... | tarlink uninstall --all",
}

var registryCommandHelp = map[string]string{
	"validate": "usage: tarlink registry validate <path>", "freshness": "usage: tarlink registry freshness <app> [--json]",
	"check":      "usage: tarlink registry check <path> [--app <id> | --old-root <path> | --all-artifacts]",
	"inspect":    "usage: tarlink registry inspect <owner/repo | release-asset-url | manifest.yaml | directory> [--json] [--refresh]",
	"add":        "usage: tarlink registry add <release-asset-url> [--non-interactive] [--json] [--dry-run] [--output <path>]",
	"candidates": "usage: tarlink registry candidates [--changed] [--json]", "blockers": "usage: tarlink registry blockers [--capability <capability>] [--json]",
	"icons": "usage: tarlink registry icons <path> [--app <id>] [--fix] [--json]",
}

// RegistryMaintainerCommand reports whether arguments select a registry
// maintainer subcommand that runs on the maintainer composition path and
// therefore must not require the application runtime. Lifecycle-backed
// registry subcommands (check, freshness) are excluded.
func RegistryMaintainerCommand(arguments []string) bool {
	if len(arguments) < 2 || arguments[0] != "registry" {
		return false
	}
	switch arguments[1] {
	case "validate", "inspect", "add", "candidates", "blockers", "icons":
		return true
	}
	return false
}

// RegistryTools is the CLI's composition slot for registry-maintainer
// capabilities. Each capability is optional; a command whose capability is
// absent reports it as unavailable. Maintainer commands must not require the
// application runtime (Service).
type RegistryTools struct {
	Validation app.RegistryValidationService
	Research   app.ResearchService
	Onboarding app.RegistryOnboardingService
	Candidates app.CandidateService
	Blockers   app.BlockerService
	Icons      app.RegistryIconService
}

type Runner struct {
	Service   app.Service
	Registry  RegistryTools
	Stdout    io.Writer
	Stderr    io.Writer
	Stdin     io.Reader
	LaunchTUI func(context.Context, app.Service, io.Writer, io.Writer) error
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
	progress := r.progress()
	r.Stdout = progressOutput{Writer: r.Stdout, finish: progress.finish}
	r.Stderr = progressOutput{Writer: r.Stderr, finish: progress.finish}
	if len(arguments) == 0 {
		if r.LaunchTUI == nil {
			return r.fail(errors.New("TUI is unavailable"))
		}
		if err := r.LaunchTUI(ctx, r.Service, r.Stdout, r.Stderr); err != nil {
			return r.fail(err)
		}
		return 0
	}
	if len(arguments) == 2 && (arguments[1] == "--help" || arguments[1] == "-h") {
		if arguments[0] == "registry" {
			_, _ = io.WriteString(r.Stdout, registryHelp)
			return 0
		}
		if usage, ok := commandHelp[arguments[0]]; ok {
			_, _ = io.WriteString(r.Stdout, usage+"\n")
			return 0
		}
	}
	if len(arguments) == 3 && arguments[0] == "registry" && (arguments[2] == "--help" || arguments[2] == "-h") {
		if usage, ok := registryCommandHelp[arguments[1]]; ok {
			_, _ = io.WriteString(r.Stdout, usage+"\n")
			return 0
		}
	}
	if r.Service == nil && !RegistryMaintainerCommand(arguments) && arguments[0] != "version" && arguments[0] != "help" && arguments[0] != "--help" && arguments[0] != "-h" {
		return r.fail(errors.New("TarLink core is unavailable"))
	}
	var err error
	switch arguments[0] {
	case "registry":
		if len(arguments) == 1 || (len(arguments) == 2 && (arguments[1] == "help" || arguments[1] == "--help" || arguments[1] == "-h")) {
			_, err = io.WriteString(r.Stdout, registryHelp)
			break
		}
		if len(arguments) == 3 && arguments[1] == "validate" {
			if r.Registry.Validation == nil {
				return r.fail(errors.New("registry validation is unavailable"))
			}
			err = r.Registry.Validation.ValidateRegistry(ctx, arguments[2])
			if err == nil {
				_, err = fmt.Fprintln(r.Stdout, "Registry is valid")
			}
			break
		}
		if len(arguments) >= 3 && arguments[1] == "check" {
			options, parseErr := registryCheckArguments(arguments[2:])
			if parseErr != nil {
				return r.invalid(registryCommandHelp["check"])
			}
			service, ok := r.Service.(app.RegistryCheckService)
			if !ok {
				return r.fail(errors.New("registry checker is unavailable"))
			}
			var result app.RegistryCheckResult
			result, err = service.CheckRegistry(ctx, options)
			if err == nil {
				_, err = fmt.Fprintf(r.Stdout, "Registry is valid; materialized %d artifact(s)\n", result.Materialized)
			}
			break
		}
		if len(arguments) >= 3 && arguments[1] == "freshness" {
			value, jsonOutput, parseErr := oneValueJSON(arguments[2:])
			if parseErr != nil {
				return r.invalid("usage: tarlink registry freshness <app> [--json]")
			}
			service, ok := r.Service.(app.FreshnessService)
			if !ok {
				return r.fail(errors.New("registry freshness is unavailable"))
			}
			var report freshness.Report
			report, err = service.Freshness(ctx, value)
			if err == nil {
				err = r.printFreshness(report, jsonOutput)
			}
			break
		}
		if len(arguments) >= 3 && arguments[1] == "inspect" {
			target := arguments[2]
			_, directErr := research.ParseReleaseAssetURL(target)
			_, localErr := os.Lstat(target)
			if directErr == nil || localErr == nil {
				target, jsonOutput, refresh, parseErr := registryInspectArguments(arguments[2:])
				if parseErr != nil {
					return r.invalid(registryCommandHelp["inspect"])
				}
				if r.Registry.Onboarding == nil {
					return r.fail(errors.New("registry onboarding is unavailable"))
				}
				value, inspectErr := r.Registry.Onboarding.InspectRegistry(ctx, app.RegistryInspectOptions{Target: target, Refresh: refresh})
				if inspectErr != nil {
					return r.fail(inspectErr)
				}
				if jsonOutput {
					err = writeJSON(r.Stdout, value)
				} else {
					err = r.printRegistryInspection(value)
				}
				break
			}
			opts, jsonOutput, parseErr := researchArguments(arguments[2:])
			if parseErr != nil {
				return r.invalid(registryCommandHelp["inspect"])
			}
			opts.Inspect = true
			if r.Registry.Research == nil {
				return r.fail(errors.New("registry research is unavailable"))
			}
			value, researchErr := r.Registry.Research.Research(ctx, opts)
			if researchErr != nil {
				if jsonOutput {
					// The app facade returns a structured ERROR result for provider
					// failures. Preserve that complete shape (including provider
					// taxonomy) on stdout while retaining the non-zero exit code.
					if value.Status == "ERROR" && value.Error != nil {
						_ = writeJSON(r.Stdout, value)
						return exitCode(researchErr)
					}
					code := string(app.CodeOf(researchErr))
					errorValue := map[string]any{"message": researchErr.Error(), "reason_code": code}
					var failure *app.ResearchFailure
					if errors.As(researchErr, &failure) {
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
					return exitCode(researchErr)
				}
				return r.fail(researchErr)
			}
			err = r.printResearch(value, jsonOutput)
			break
		}
		if len(arguments) >= 3 && arguments[1] == "add" {
			options, jsonOutput, dryRun, output, parseErr := registryAddArguments(arguments[2:])
			if parseErr != nil {
				return r.invalid(registryCommandHelp["add"])
			}
			if r.Registry.Onboarding == nil {
				return r.fail(errors.New("registry onboarding is unavailable"))
			}
			value, addErr := r.Registry.Onboarding.AddRegistry(ctx, options)
			if addErr != nil {
				return r.fail(addErr)
			}
			if !options.NonInteractive && value.Status == "needs-input" {
				if err = r.printCandidate(value.Candidate); err != nil {
					break
				}
				options, value.Candidate, err = r.promptRegistryAdd(options, value.Candidate, value.Required)
				if err == nil {
					value, err = app.CompleteRegistryCandidate(value.Candidate, options)
				}
			}
			if jsonOutput {
				err = writeJSON(r.Stdout, value)
				if value.Status == "needs-input" && err == nil {
					return 2
				}
				break
			}
			if err != nil {
				break
			}
			if value.Status == "needs-input" {
				return r.fail(fmt.Errorf("registry add needs input: %s", requiredFields(value.Required)))
			}
			if dryRun {
				_, err = fmt.Fprintf(r.Stdout, "Candidate manifest (dry run):\n%s", value.YAML)
				break
			}
			if output != "" {
				err = writeNewFile(output, value.YAML)
				if err == nil {
					_, err = fmt.Fprintf(r.Stdout, "Wrote candidate manifest to %s\n", output)
				}
				break
			}
			_, err = r.Stdout.Write(value.YAML)
			break
		}
		if arguments[1] == "candidates" {
			changed, jsonOutput, parseErr := candidateArguments(arguments[2:])
			if parseErr != nil {
				return r.invalid("usage: tarlink registry candidates [--changed] [--json]")
			}
			if r.Registry.Candidates == nil {
				return r.fail(errors.New("candidate ledger is unavailable"))
			}
			if changed {
				var v research.CandidateChanges
				v, err = r.Registry.Candidates.CandidateChanges(ctx)
				if err == nil {
					if jsonOutput {
						err = writeJSON(r.Stdout, v)
					} else {
						err = printChanges(r.Stdout, v)
					}
				}
			} else {
				var v research.CandidateLedger
				v, err = r.Registry.Candidates.CandidateLedger()
				if err == nil {
					if jsonOutput {
						err = writeJSON(r.Stdout, v)
					} else {
						for _, c := range v.Candidates {
							_, err = fmt.Fprintf(r.Stdout, "%-24s %-10s %s\n", c.ID, c.Status, c.Upstream)
							if err != nil {
								break
							}
						}
					}
				}
			}
			break
		}
		if arguments[1] == "blockers" {
			capability, jsonOutput, parseErr := blockerArguments(arguments[2:])
			if parseErr != nil {
				return r.invalid("usage: tarlink registry blockers [--capability <capability>] [--json]")
			}
			if r.Registry.Blockers == nil {
				return r.fail(errors.New("blocker analysis is unavailable"))
			}
			if capability == "" {
				var v []research.BlockerSummary
				v, err = r.Registry.Blockers.Blockers("")
				if err == nil {
					if jsonOutput {
						err = writeJSON(r.Stdout, v)
					} else {
						for _, x := range v {
							_, err = fmt.Fprintf(r.Stdout, "%-32s %d\n", x.Blocker, x.Count)
							if err != nil {
								break
							}
						}
					}
				}
			} else {
				var v []research.CapabilityResult
				v, err = r.Registry.Blockers.CapabilityPreflight(capability)
				if err == nil {
					if jsonOutput {
						err = writeJSON(r.Stdout, v)
					} else {
						err = printCapability(r.Stdout, v)
					}
				}
			}
			break
		}
		if arguments[1] == "icons" {
			options, jsonOutput, parseErr := registryIconArguments(arguments[2:])
			if parseErr != nil {
				return r.invalid(registryCommandHelp["icons"])
			}
			if r.Registry.Icons == nil {
				return r.fail(errors.New("registry icon maintenance is unavailable"))
			}
			var report app.RegistryIconReport
			report, err = r.Registry.Icons.RegistryIcons(ctx, options)
			if err == nil {
				if jsonOutput {
					err = writeJSON(r.Stdout, report)
				} else {
					for _, result := range report.Results {
						_, err = fmt.Fprintf(r.Stdout, "%-24s %-8s %s\n", result.App, result.Status, result.Error)
						if err != nil {
							break
						}
					}
				}
			}
			break
		}
		return r.invalid("usage: tarlink registry <validate|check|freshness|inspect|add|candidates|blockers|icons> ...")
	case "refresh":
		if len(arguments) != 1 {
			return r.invalid("usage: tarlink refresh")
		}
		var checkedAt time.Time
		checkedAt, err = r.Service.SyncRegistry(ctx, progress.report)
		if err == nil {
			_, err = fmt.Fprintf(r.Stdout, "Application catalog refreshed. Checked at %s.\n", checkedAt.UTC().Format(time.RFC3339))
		}
	case "search":
		value, jsonOutput, parseErr := oneValueJSON(arguments[1:])
		if parseErr != nil {
			return r.invalid("usage: tarlink search <query> [--json]")
		}
		var result []app.Application
		result, err = r.Service.Search(ctx, value)
		if err == nil {
			err = r.printApplications(result, jsonOutput, "No applications found.")
		}
	case "install":
		options, parseErr := installArguments(arguments[1:])
		if parseErr != nil {
			return r.invalid(commandHelp["install"])
		}
		if options.file != "" {
			service, ok := r.Service.(app.LockService)
			if !ok {
				return r.fail(errors.New("lockfile installation is unavailable"))
			}
			var result app.BatchResult
			result, err = service.InstallLock(ctx, options.file, options.forcePath, progress.report)
			if err == nil {
				err = r.printBatch("Installed", result)
			}
			break
		}
		if len(options.apps) > 1 {
			service, ok := r.Service.(app.BatchInstallOptionsService)
			if !ok {
				return r.fail(errors.New("batch installation is unavailable"))
			}
			var result app.BatchResult
			result, err = service.InstallBatchWithOptions(ctx, options.apps, options.forcePath, progress.report)
			if err == nil {
				err = r.printBatch("Installed", result)
			}
			break
		}
		value := options.apps[0]
		// PATH preflight concerns the application identity, not the requested
		// release/channel. Keep the original selector for Install below.
		selector, selectorErr := app.ParseSelector(value)
		if selectorErr != nil {
			return r.invalid(commandHelp["install"])
		}
		conflicts, checkErr := r.Service.CheckInstallPath(selector.App)
		if checkErr != nil {
			err = checkErr
			break
		}
		if len(conflicts) != 0 && !options.forcePath {
			if err := r.printPathConflicts(value, conflicts); err != nil {
				return r.fail(err)
			}
			_, _ = fmt.Fprintf(r.Stderr, "Refusing to install %s because a PATH conflict was detected.\nRe-run with --force-path to acknowledge and install anyway.\n", value)
			return exitConflict
		}
		var result app.Result
		result, err = r.Service.Install(ctx, value, progress.report)
		if err == nil {
			err = r.printResult("Installed", result)
		}
	case "lock":
		output, parseErr := lockArguments(arguments[1:])
		if parseErr != nil {
			return r.invalid(commandHelp["lock"])
		}
		service, ok := r.Service.(app.LockService)
		if !ok {
			return r.fail(errors.New("lockfile snapshots are unavailable"))
		}
		err = service.WriteLock(ctx, output)
		if err == nil {
			if output == "" {
				output = "tarlink.lock"
			}
			_, err = fmt.Fprintf(r.Stdout, "Wrote lock snapshot to %s\n", output)
		}
	case "update":
		if len(arguments) == 2 && arguments[1] == "--all" {
			var result app.UpdateAllResult
			result, err = r.Service.UpdateAll(ctx, progress.report)
			if err == nil {
				err = r.printUpdateAll(result)
			}
			break
		}
		value, parseErr := oneValue(arguments[1:])
		if parseErr != nil {
			return r.invalid("usage: tarlink update <app> | tarlink update --all")
		}
		var result app.Result
		result, err = r.Service.Update(ctx, value, progress.report)
		if err == nil {
			err = r.printResult("Updated", result)
		}
	case "pin", "unpin":
		value, parseErr := oneValue(arguments[1:])
		if parseErr != nil {
			return r.invalid("usage: tarlink " + arguments[0] + " <app>")
		}
		service, ok := r.Service.(interface {
			Pin(context.Context, string) error
			Unpin(context.Context, string) error
		})
		if !ok {
			return r.fail(errors.New("pinning is unavailable"))
		}
		if arguments[0] == "pin" {
			err = service.Pin(ctx, value)
		} else {
			err = service.Unpin(ctx, value)
		}
		if err == nil {
			_, err = fmt.Fprintf(r.Stdout, "%s %s\n", strings.Title(arguments[0]), value)
		}
	case "self-update":
		if len(arguments) != 1 {
			return r.invalid("usage: tarlink self-update")
		}
		var value app.TarLinkVersion
		value, err = r.Service.UpgradeTarLink(ctx, progress.report)
		if err == nil {
			if !value.UpgradeAvailable {
				_, err = fmt.Fprintf(r.Stdout, "TarLink %s is already up to date.\n", value.Current)
			} else {
				_, err = fmt.Fprintf(r.Stdout, "TarLink %s → %s\nTarLink upgraded to %s\n", value.Current, value.Latest, value.Latest)
			}
		}
	case "doctor":
		if len(arguments) != 1 {
			return r.invalid("usage: tarlink doctor")
		}
		var report app.DoctorReport
		report, err = r.Service.Doctor(ctx)
		if err == nil {
			err = r.printDoctor(report)
			if err == nil && report.Errors > 0 {
				err = &app.Error{Code: app.CodeStateCorrupt, Op: "doctor", Err: errors.New("integrity errors found")}
			}
		}
	case "list":
		installedOnly, updatesOnly, jsonOutput, parseErr := listArguments(arguments[1:])
		if parseErr != nil {
			return r.invalid("usage: tarlink list [--installed|--updates] [--json]")
		}
		var result []app.Application
		result, err = r.Service.ListAvailable(ctx)
		if err == nil {
			if installedOnly || updatesOnly {
				filtered := make([]app.Application, 0, len(result))
				for _, value := range result {
					if value.InstalledVersion != "" && (!updatesOnly || value.UpdateAvailable) {
						filtered = append(filtered, value)
					}
				}
				result = filtered
			}
			emptyMessage := "No applications found."
			if installedOnly {
				emptyMessage = "No installed applications."
			} else if updatesOnly {
				emptyMessage = "No updates available."
			}
			err = r.printApplications(result, jsonOutput, emptyMessage)
		}
	case "info":
		value, jsonOutput, parseErr := oneValueJSON(arguments[1:])
		if parseErr != nil {
			return r.invalid("usage: tarlink info <app> [--json]")
		}
		var result app.Application
		result, err = r.Service.Info(ctx, value)
		if err == nil {
			err = r.printInfo(result, jsonOutput)
		}
	case "versions":
		value, jsonOutput, parseErr := oneValueJSON(arguments[1:])
		if parseErr != nil {
			return r.invalid("usage: tarlink versions <app> [--json]")
		}
		var result []app.Version
		result, err = r.Service.Versions(ctx, value)
		if err == nil {
			err = r.printVersions(value, result, jsonOutput)
		}
	case "rollback":
		value, parseErr := oneValue(arguments[1:])
		if parseErr != nil {
			return r.invalid("usage: tarlink rollback <app>")
		}
		var result app.Result
		result, err = r.Service.Rollback(ctx, value, progress.report)
		if err == nil {
			err = r.printResult("Rolled back", result)
		}
	case "uninstall":
		if len(arguments) == 2 && arguments[1] == "--all" {
			var result app.UninstallAllResult
			var uninstallErr error
			result, uninstallErr = r.Service.UninstallAll(ctx, progress.report)
			printErr := r.printUninstallAll(result)
			if printErr != nil {
				err = printErr
			} else {
				err = uninstallErr
			}
			break
		}
		values, parseErr := uninstallArguments(arguments[1:])
		if parseErr != nil {
			return r.invalid(commandHelp["uninstall"])
		}
		if len(values) > 1 {
			service, ok := r.Service.(app.BatchService)
			if !ok {
				return r.fail(errors.New("batch uninstallation is unavailable"))
			}
			var result app.BatchResult
			result, err = service.UninstallBatch(ctx, values, progress.report)
			if err == nil {
				err = r.printBatch("Uninstalled", result)
			}
			break
		}
		value := values[0]
		var result app.Result
		result, err = r.Service.Uninstall(ctx, value, progress.report)
		if err == nil {
			err = r.printResult("Uninstalled", result)
		}
	case "version":
		if len(arguments) != 1 {
			return r.invalid("usage: tarlink version")
		}
		_, err = fmt.Fprintf(r.Stdout, "tarlink %s\n", version.Current)
	case "help", "--help", "-h":
		if len(arguments) == 2 && arguments[1] == "registry" {
			_, err = io.WriteString(r.Stdout, registryHelp)
			break
		}
		if len(arguments) == 2 {
			if usage, ok := commandHelp[arguments[1]]; ok {
				_, err = io.WriteString(r.Stdout, usage+"\n")
				break
			}
		}
		if len(arguments) != 1 {
			return r.invalid("usage: tarlink help [registry]")
		}
		_, err = io.WriteString(r.Stdout, help)
	default:
		return r.invalid("unknown command; run `tarlink help`")
	}
	if err != nil {
		return r.fail(err)
	}
	return 0
}

func (r Runner) progress() *progressRenderer {
	return newProgressRenderer(r.Stderr, isTTY(r.Stderr))
}

type progressRenderer struct {
	writer io.Writer
	tty    bool
	last   app.ProgressStage
	count  int
	active bool
}

func newProgressRenderer(writer io.Writer, tty bool) *progressRenderer {
	return &progressRenderer{writer: writer, tty: tty}
}

func (p *progressRenderer) report(event app.Progress) {
	if event.Stage == p.last || event.Stage == "" || p.count >= 16 {
		return
	}
	p.last = event.Stage
	p.count++
	label := event.Description
	if label == "" {
		label = title(string(event.Stage))
	}
	if event.BytesTotal > 0 {
		label = fmt.Sprintf("%s %s / %s", label, bytesLabel(event.BytesDone), bytesLabel(event.BytesTotal))
	} else if event.BytesDone > 0 {
		label = fmt.Sprintf("%s %s", label, bytesLabel(event.BytesDone))
	}
	if p.tty {
		_, _ = fmt.Fprintf(p.writer, "\r%-80s", label)
		p.active = true
		return
	}
	_, _ = fmt.Fprintln(p.writer, label)
}

func (p *progressRenderer) finish() {
	if p.tty && p.active {
		_, _ = fmt.Fprintln(p.writer)
		p.active = false
	}
}

type progressOutput struct {
	io.Writer
	finish func()
}

func (w progressOutput) Write(value []byte) (int, error) {
	w.finish()
	return w.Writer.Write(value)
}

func isTTY(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func bytesLabel(value int64) string {
	if value < 0 {
		value = 0
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	amount := float64(value)
	i := 0
	for amount >= 1024 && i < len(units)-1 {
		amount /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d %s", value, units[i])
	}
	return fmt.Sprintf("%.1f %s", amount, units[i])
}

func (r Runner) printApplications(values []app.Application, jsonOutput bool, emptyMessage string) error {
	values = append([]app.Application{}, values...)
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	if jsonOutput {
		return writeJSON(r.Stdout, values)
	}
	if len(values) == 0 {
		_, err := fmt.Fprintf(r.Stdout, "%s\n", emptyMessage)
		return err
	}
	for _, value := range values {
		status := "available"
		if value.InstalledVersion != "" {
			status = "installed " + value.InstalledVersion
			if value.UpdateAvailable {
				status += ", update available"
			}
		}
		if hasGameData(value) {
			status += " [GAME DATA]"
		}
		if _, err := fmt.Fprintf(r.Stdout, "%-20s %-12s %s\n", value.ID, value.RegistryVersion, status); err != nil {
			return err
		}
	}
	return nil
}

func (r Runner) printInfo(value app.Application, jsonOutput bool) error {
	if jsonOutput {
		return writeJSON(r.Stdout, value)
	}
	update := "none"
	if value.UpdateAvailable {
		update = "available"
	}
	installed := value.InstalledVersion
	if installed == "" {
		installed = "not installed"
	}
	requirements := ""
	if hasGameData(value) {
		requirements = "Requires:     Original game data\n"
	}
	_, err := fmt.Fprintf(r.Stdout, "%s\n\nID:          %s\nVersion:     %s\nInstalled:   %s\nUpdate:      %s\nCategories:  %s\n%sHomepage:    %s\n",
		value.Name, value.ID, value.RegistryVersion, installed, update, strings.Join(value.Categories, ", "), requirements, value.Homepage)
	return err
}

func hasGameData(value app.Application) bool {
	for _, requirement := range value.Requirements {
		if requirement == "original-game-data" {
			return true
		}
	}
	return false
}

func (r Runner) printVersions(id string, values []app.Version, jsonOutput bool) error {
	if jsonOutput {
		return writeJSON(r.Stdout, values)
	}
	if _, err := fmt.Fprintf(r.Stdout, "%s\n\n", id); err != nil {
		return err
	}
	for _, value := range values {
		if _, err := fmt.Fprintf(r.Stdout, "%-20s %s\n", value.Version, value.Status); err != nil {
			return err
		}
	}
	return nil
}

func (r Runner) printFreshness(report freshness.Report, jsonOutput bool) error {
	if jsonOutput {
		return writeJSON(r.Stdout, report)
	}
	if len(report.Candidates) == 0 {
		_, err := io.WriteString(r.Stdout, "No upstream release candidates found.\n")
		return err
	}
	for _, candidate := range report.Candidates {
		if _, err := fmt.Fprintf(r.Stdout, "%s@%s %s (%s)\n", candidate.App, candidate.Channel, candidate.Version, candidate.UpstreamURL); err != nil {
			return err
		}
	}
	return nil
}

func researchArguments(arguments []string) (app.ResearchOptions, bool, error) {
	if len(arguments) == 0 {
		return app.ResearchOptions{}, false, errors.New("repository is required")
	}
	opts := app.ResearchOptions{Repository: arguments[0]}
	jsonOutput := false
	for i := 1; i < len(arguments); i++ {
		switch arguments[i] {
		case "--json":
			jsonOutput = true
		case "--refresh":
			opts.Refresh = true
		case "--release", "--asset":
			if i+1 >= len(arguments) || strings.HasPrefix(arguments[i+1], "--") {
				return app.ResearchOptions{}, false, errors.New("selector value is required")
			}
			if arguments[i] == "--release" {
				opts.Release = arguments[i+1]
			} else {
				opts.Asset = arguments[i+1]
			}
			i++
		default:
			return app.ResearchOptions{}, false, errors.New("unknown research option")
		}
	}
	return opts, jsonOutput, nil
}

func registryInspectArguments(arguments []string) (string, bool, bool, error) {
	if len(arguments) == 0 || strings.HasPrefix(arguments[0], "--") {
		return "", false, false, errors.New("target required")
	}
	target, jsonOutput, refresh := arguments[0], false, false
	for _, value := range arguments[1:] {
		switch value {
		case "--json":
			jsonOutput = true
		case "--refresh":
			refresh = true
		default:
			return "", false, false, errors.New("unknown option")
		}
	}
	return target, jsonOutput, refresh, nil
}

func registryAddArguments(arguments []string) (app.RegistryAddOptions, bool, bool, string, error) {
	if len(arguments) == 0 || strings.HasPrefix(arguments[0], "--") {
		return app.RegistryAddOptions{}, false, false, "", errors.New("release asset URL required")
	}
	options := app.RegistryAddOptions{Target: arguments[0]}
	jsonOutput, dryRun, output := false, false, ""
	for i := 1; i < len(arguments); i++ {
		switch arguments[i] {
		case "--non-interactive":
			options.NonInteractive = true
		case "--json":
			jsonOutput = true
		case "--dry-run":
			dryRun = true
		case "--refresh":
			options.Refresh = true
		case "--id", "--name", "--summary", "--categories", "--output":
			if i+1 >= len(arguments) || strings.HasPrefix(arguments[i+1], "--") {
				return app.RegistryAddOptions{}, false, false, "", errors.New("option value required")
			}
			value := arguments[i+1]
			switch arguments[i] {
			case "--id":
				options.ID = value
			case "--name":
				options.Name = value
			case "--summary":
				options.Summary = value
			case "--categories":
				options.Categories = splitCSV(value)
			case "--output":
				output = value
			}
			i++
		case "--create-bin-link":
			value := true
			options.CreateBinLink = &value
		case "--no-create-bin-link":
			value := false
			options.CreateBinLink = &value
		default:
			return app.RegistryAddOptions{}, false, false, "", errors.New("unknown option")
		}
	}
	if jsonOutput && !options.NonInteractive {
		return app.RegistryAddOptions{}, false, false, "", errors.New("--json requires --non-interactive")
	}
	if dryRun && output != "" {
		return app.RegistryAddOptions{}, false, false, "", errors.New("--dry-run and --output are mutually exclusive")
	}
	return options, jsonOutput, dryRun, output, nil
}

func splitCSV(value string) []string {
	var result []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func (r Runner) promptRegistryAdd(options app.RegistryAddOptions, candidate app.RegistryCandidate, required []app.RegistryRequiredInput) (app.RegistryAddOptions, app.RegistryCandidate, error) {
	reader := bufio.NewReader(r.Stdin)
	needs := map[string]bool{}
	for _, value := range required {
		needs[value.Field] = true
	}
	if needs["executable"] {
		selected, err := r.promptCandidate(reader, "Executable", candidate.Executables, false)
		if err != nil {
			return options, candidate, err
		}
		candidate.Executable = selected
	}
	if needs["icon"] {
		selected, err := r.promptCandidate(reader, "Archive icon", candidate.Icons, true)
		if err != nil {
			return options, candidate, err
		}
		candidate.Icon = selected
	}
	if needs["platform"] || needs["archive"] || needs["artifact"] || needs["nested-archive"] {
		return options, candidate, errors.New("candidate has unresolved artifact facts; supply a less ambiguous official release asset")
	}
	if len(options.Categories) == 0 {
		value, err := r.promptLine(reader, "Category (comma-separated): ")
		if err != nil {
			return options, candidate, err
		}
		options.Categories = splitCSV(value)
		if len(options.Categories) == 0 {
			return options, candidate, errors.New("at least one category is required")
		}
	}
	if options.CreateBinLink == nil && (containsCategory(options.Categories, "games") || containsCategory(options.Categories, "recompilation")) {
		value, err := r.promptLine(reader, "Create CLI bin link? [y/N]: ")
		if err != nil {
			return options, candidate, err
		}
		answer := strings.ToLower(strings.TrimSpace(value))
		selected := answer == "y" || answer == "yes"
		options.CreateBinLink = &selected
	}
	return options, candidate, nil
}

func (r Runner) promptLine(reader *bufio.Reader, label string) (string, error) {
	if _, err := io.WriteString(r.Stdout, label); err != nil {
		return "", err
	}
	value, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(value), nil
}
func (r Runner) promptCandidate(reader *bufio.Reader, label string, candidates []string, optional bool) (string, error) {
	for index, value := range candidates {
		if _, err := fmt.Fprintf(r.Stdout, "%s %d: %s\n", label, index+1, value); err != nil {
			return "", err
		}
	}
	suffix := ""
	if optional {
		suffix = " (0 for none)"
	}
	value, err := r.promptLine(reader, label+" selection"+suffix+": ")
	if err != nil {
		return "", err
	}
	if optional && (value == "" || value == "0") {
		return "", nil
	}
	var index int
	if _, err := fmt.Sscan(value, &index); err != nil || index < 1 || index > len(candidates) {
		return "", errors.New("invalid candidate selection")
	}
	return candidates[index-1], nil
}
func containsCategory(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func (r Runner) printCandidate(value app.RegistryCandidate) error {
	_, err := fmt.Fprintf(r.Stdout, "GitHub repository       ✓ %s\nRelease                 ✓ %s\nAsset                   ✓ %s\nPlatform                %s\nArchive                 %s\nSHA-256                 %s\nExecutable              %s\nIcon                    %s\n\n", value.Repository, value.Release, value.Asset, checkValue(value.Platform), checkValue(value.Archive), checkValue(value.SHA256), checkValue(value.Executable), checkValue(value.Icon))
	return err
}
func checkValue(value string) string {
	if value == "" {
		return "?"
	}
	return "✓ " + value
}
func requiredFields(values []app.RegistryRequiredInput) string {
	fields := make([]string, 0, len(values))
	for _, value := range values {
		fields = append(fields, value.Field)
	}
	return strings.Join(fields, ", ")
}

func (r Runner) printRegistryInspection(value app.RegistryInspectionResult) error {
	if value.Manifest != nil {
		mark := "✓"
		if !value.Manifest.Valid {
			mark = "✗"
		}
		if _, err := fmt.Fprintf(r.Stdout, "%s\n\n", value.Manifest.ID); err != nil {
			return err
		}
		if !value.Manifest.Valid {
			_, err := fmt.Fprintf(r.Stdout, "%s %s\n", mark, value.Manifest.Error)
			return err
		}
		for _, check := range value.Manifest.Checks {
			if _, err := fmt.Fprintf(r.Stdout, "%s %s\n", mark, check); err != nil {
				return err
			}
		}
		return nil
	}
	if value.Directory != nil {
		if _, err := fmt.Fprintf(r.Stdout, "Registry inspection\n\nManifests: %d\nValid:     %d\nWarnings:  %d\nInvalid:   %d\n\n", value.Directory.Manifests, value.Directory.Valid, value.Directory.Warnings, value.Directory.Invalid); err != nil {
			return err
		}
		for _, result := range value.Directory.Results {
			mark := "✓"
			if !result.Valid {
				mark = "✗"
			}
			if _, err := fmt.Fprintf(r.Stdout, "%s %s\n", mark, result.Path); err != nil {
				return err
			}
		}
		return nil
	}
	if value.Candidate != nil {
		if err := r.printCandidate(*value.Candidate); err != nil {
			return err
		}
		if len(value.Required) != 0 {
			_, err := fmt.Fprintf(r.Stdout, "Needs input: %s\n", requiredFields(value.Required))
			return err
		}
		return nil
	}
	return nil
}

func writeNewFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(data)
	return err
}

func candidateArguments(a []string) (bool, bool, error) {
	changed, jsonOut := false, false
	for _, v := range a {
		switch v {
		case "--changed":
			changed = true
		case "--json":
			jsonOut = true
		default:
			return false, false, errors.New("unknown option")
		}
	}
	return changed, jsonOut, nil
}
func blockerArguments(a []string) (string, bool, error) {
	capability, jsonOut := "", false
	for i := 0; i < len(a); i++ {
		switch a[i] {
		case "--json":
			jsonOut = true
		case "--capability":
			if i+1 >= len(a) || strings.HasPrefix(a[i+1], "-") {
				return "", false, errors.New("capability required")
			}
			capability = a[i+1]
			i++
		default:
			return "", false, errors.New("unknown option")
		}
	}
	return capability, jsonOut, nil
}

func registryIconArguments(arguments []string) (app.RegistryIconOptions, bool, error) {
	if len(arguments) == 0 || strings.HasPrefix(arguments[0], "--") {
		return app.RegistryIconOptions{}, false, errors.New("path required")
	}
	options := app.RegistryIconOptions{Root: arguments[0]}
	jsonOutput := false
	for i := 1; i < len(arguments); i++ {
		switch arguments[i] {
		case "--fix":
			options.Fix = true
		case "--json":
			jsonOutput = true
		case "--app":
			if i+1 >= len(arguments) || strings.HasPrefix(arguments[i+1], "--") {
				return app.RegistryIconOptions{}, false, errors.New("app required")
			}
			options.App = arguments[i+1]
			i++
		default:
			return app.RegistryIconOptions{}, false, errors.New("unknown option")
		}
	}
	return options, jsonOutput, nil
}

func registryCheckArguments(arguments []string) (app.RegistryCheckOptions, error) {
	if len(arguments) == 0 || strings.HasPrefix(arguments[0], "-") {
		return app.RegistryCheckOptions{}, errors.New("registry path required")
	}
	options := app.RegistryCheckOptions{Root: arguments[0]}
	selector := 0
	for i := 1; i < len(arguments); i++ {
		switch arguments[i] {
		case "--app":
			if i+1 >= len(arguments) || strings.HasPrefix(arguments[i+1], "-") {
				return app.RegistryCheckOptions{}, errors.New("application required")
			}
			options.App = arguments[i+1]
			selector++
			i++
		case "--old-root":
			if i+1 >= len(arguments) || strings.HasPrefix(arguments[i+1], "-") {
				return app.RegistryCheckOptions{}, errors.New("previous registry path required")
			}
			options.OldRoot = arguments[i+1]
			selector++
			i++
		case "--all-artifacts":
			options.AllArtifacts = true
			selector++
		default:
			return app.RegistryCheckOptions{}, errors.New("unknown option")
		}
	}
	if selector > 1 {
		return app.RegistryCheckOptions{}, errors.New("registry check selectors are mutually exclusive")
	}
	return options, nil
}

func batchInspectArguments(a []string) (string, bool, error) {
	if len(a) == 0 {
		return "", false, errors.New("path required")
	}
	if len(a) == 1 {
		return a[0], false, nil
	}
	if len(a) == 2 && a[1] == "--json" {
		return a[0], true, nil
	}
	return "", false, errors.New("invalid batch options")
}
func printChanges(w io.Writer, v research.CandidateChanges) error {
	_, e := fmt.Fprintf(w, "RECHECK %d\nUNCHANGED %d\nERROR %d\n", v.Summary["RECHECK"], v.Summary["UNCHANGED"], v.Summary["ERROR"])
	if e != nil {
		return e
	}
	for _, x := range v.Results {
		if x.Decision == "RECHECK" || x.Decision == "ERROR" {
			old := fmt.Sprintf("%s (%d)", x.Old.ReleaseTag, x.Old.ReleaseID)
			current := ""
			if x.Current != nil {
				current = fmt.Sprintf(" -> %s (%d)", x.Current.ReleaseTag, x.Current.ReleaseID)
			}
			if _, e = fmt.Fprintf(w, "%-10s %s %s%s %s\n", x.Decision, x.ID, old, current, x.Reason); e != nil {
				return e
			}
		}
	}
	return nil
}
func printCapability(w io.Writer, v []research.CapabilityResult) error {
	for _, x := range v {
		if _, e := fmt.Fprintf(w, "%s\n  removed: %s\n  remaining: %s\n  fully unlocked: %t\n", x.ID, strings.Join(x.Removed, ", "), strings.Join(x.Remaining, ", "), x.FullyUnlocked); e != nil {
			return e
		}
	}
	return nil
}

func (r Runner) printResearch(value app.ResearchResult, jsonOutput bool) error {
	if jsonOutput {
		// ResearchResult is the sole JSON model for both commands. In
		// particular, status is derived by the application facade on every
		// invocation and is never treated as cached approval.
		return writeJSON(r.Stdout, value)
	}
	_, err := fmt.Fprintf(r.Stdout, "Repository: %s\nRelease tag: %s\nRelease ID: %d\nAsset: %s\nAsset ID: %d\nAsset size: %d\nGitHub digest: %s\nAlgorithm: %s\nVerdict: %s\nReason: %s\n", value.Repository, value.Release.Tag, value.Release.ID, value.Asset.Name, value.Asset.ID, value.Asset.Size, value.Asset.Digest, value.Provenance.Algorithm, value.Provenance.Verdict, value.Provenance.Message)
	if err != nil || value.Inspection == nil {
		return err
	}
	if len(value.Inspection.ComputedDigests) != 0 {
		if _, err = fmt.Fprintf(r.Stdout, "Computed digests: sha256=%s sha512=%s\n", value.Inspection.ComputedDigests["sha256"], value.Inspection.ComputedDigests["sha512"]); err != nil {
			return err
		}
	}
	if _, err = fmt.Fprintf(r.Stdout, "Status: %s\nArtifact type: %s\nExecutables: %s\nNested archives: %s\nBlockers: %s\n", value.Status, value.Inspection.ArtifactType, strings.Join(value.Inspection.Executables, ", "), strings.Join(value.Inspection.Nested, ", "), strings.Join(value.Inspection.Blockers, ", ")); err != nil {
		return err
	}
	return err
}

func (r Runner) printUpdateAll(result app.UpdateAllResult) error {
	for _, value := range result.Updated {
		if _, err := fmt.Fprintf(r.Stdout, "Updated %s to %s\n", value.AppID, value.Version); err != nil {
			return err
		}
		for _, warning := range value.Warnings {
			if _, err := fmt.Fprintf(r.Stderr, "Warning: %s\n", warning); err != nil {
				return err
			}
		}
	}
	for _, id := range result.Skipped {
		label := "No update for"
		for _, pinned := range result.Pinned {
			if pinned == id {
				label = "Skipped pinned"
				break
			}
		}
		if _, err := fmt.Fprintf(r.Stdout, "%s %s\n", label, id); err != nil {
			return err
		}
	}
	failedIDs := make([]string, 0, len(result.Failed))
	for id := range result.Failed {
		failedIDs = append(failedIDs, id)
	}
	sort.Strings(failedIDs)
	for _, id := range failedIDs {
		message := result.Failed[id]
		if _, err := fmt.Fprintf(r.Stderr, "Failed %s: %s\n", id, message); err != nil {
			return err
		}
	}
	if len(result.Failed) != 0 {
		return &app.Error{Code: result.FailureCodes[failedIDs[0]], Op: "update all", Err: errors.New("one or more applications failed to update")}
	}
	return nil
}

func (r Runner) printBatch(action string, result app.BatchResult) error {
	for _, outcome := range result.Outcomes {
		switch outcome.Status {
		case "completed":
			if outcome.Result != nil {
				if action == "Uninstalled" && outcome.Result.Version != "" {
					if _, err := fmt.Fprintf(r.Stdout, "Uninstalled %s %s\n", outcome.Result.AppID, outcome.Result.Version); err != nil {
						return err
					}
					for _, warning := range outcome.Result.Warnings {
						if _, err := fmt.Fprintf(r.Stderr, "Warning: %s\n", warning); err != nil {
							return err
						}
					}
				} else if err := r.printResult(action, *outcome.Result); err != nil {
					return err
				}
			}
		case "skipped":
			if _, err := fmt.Fprintf(r.Stdout, "Skipped %s (%s)\n", outcome.AppID, outcome.Reason); err != nil {
				return err
			}
		case "failed":
			if _, err := fmt.Fprintf(r.Stderr, "Failed %s: %s\n", outcome.AppID, outcome.Reason); err != nil {
				return err
			}
		}
	}
	if len(result.Failed) == 0 {
		return nil
	}
	for _, outcome := range result.Outcomes {
		if outcome.Status == "failed" {
			return &app.Error{Code: outcome.Code, Op: "batch operation", Err: errors.New("one or more applications failed")}
		}
	}
	return &app.Error{Code: app.CodeConflict, Op: "batch operation", Err: errors.New("one or more applications failed")}
}

func (r Runner) printUninstallAll(result app.UninstallAllResult) error {
	batch := app.BatchResult{Completed: result.Completed, Failed: result.Failed, FailureCodes: result.FailureCodes, Outcomes: result.Outcomes}
	batchErr := r.printBatch("Uninstalled", batch)
	for _, warning := range result.Warnings {
		if _, err := fmt.Fprintf(r.Stderr, "Warning: %s\n", warning); err != nil {
			return err
		}
	}
	if len(result.Outcomes) == 0 {
		_, err := io.WriteString(r.Stdout, "Uninstalled all applications\n")
		return err
	}
	return batchErr
}

func (r Runner) printDoctor(report app.DoctorReport) error {
	if _, err := io.WriteString(r.Stdout, "TarLink doctor\n\nGlobal\n"); err != nil {
		return err
	}
	print := func(check app.DoctorCheck, indent string) error {
		mark := "✓"
		if check.Status == "warning" {
			mark = "⚠"
		}
		if check.Status == "error" {
			mark = "✗"
		}
		_, err := fmt.Fprintf(r.Stdout, "%s%s %s", indent, mark, check.Label)
		if err != nil {
			return err
		}
		if check.Detail != "" {
			_, err = fmt.Fprintf(r.Stdout, ": %s", check.Detail)
		}
		if err == nil {
			_, err = io.WriteString(r.Stdout, "\n")
		}
		return err
	}
	for _, check := range report.Global {
		if err := print(check, "  "); err != nil {
			return err
		}
	}
	for _, application := range report.Applications {
		if _, err := fmt.Fprintf(r.Stdout, "\n%s\n", application.ID); err != nil {
			return err
		}
		for _, check := range application.Checks {
			if err := print(check, "  "); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintf(r.Stdout, "\nSummary: %d error(s), %d warning(s)\n", report.Errors, report.Warnings)
	return err
}

func (r Runner) printResult(action string, result app.Result) error {
	if result.Version != "" {
		preposition := " "
		if action == "Updated" || action == "Rolled back" {
			preposition = " to "
		}
		if _, err := fmt.Fprintf(r.Stdout, "%s %s%s%s\n", action, result.AppID, preposition, result.Version); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintf(r.Stdout, "%s %s\n", action, result.AppID); err != nil {
		return err
	}
	for _, warning := range result.Warnings {
		if _, err := fmt.Fprintf(r.Stderr, "Warning: %s\n", warning); err != nil {
			return err
		}
	}
	return nil
}

func (r Runner) invalid(message string) int {
	_, _ = fmt.Fprintln(r.Stderr, message)
	return exitInvalidArguments
}

func (r Runner) fail(err error) int {
	_, _ = fmt.Fprintf(r.Stderr, "tarlink: %s\n", err)
	return exitCode(err)
}

// Fail presents a startup error with the same stable exit mapping as command
// failures. It exists so the executable can enforce platform and root policy
// before constructing the application service.
func (r Runner) Fail(err error) int {
	if r.Stderr == nil {
		r.Stderr = io.Discard
	}
	return r.fail(err)
}

func writeJSON(destination io.Writer, value any) error {
	encoder := json.NewEncoder(destination)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func oneValue(arguments []string) (string, error) {
	if len(arguments) != 1 || arguments[0] == "" || strings.HasPrefix(arguments[0], "-") {
		return "", errors.New("one value required")
	}
	return arguments[0], nil
}

type installOptions struct {
	apps      []string
	file      string
	forcePath bool
}

func installArguments(arguments []string) (installOptions, error) {
	var options installOptions
	for index := 0; index < len(arguments); index++ {
		value := arguments[index]
		switch value {
		case "--force-path":
			if options.forcePath {
				return installOptions{}, errors.New("duplicate --force-path")
			}
			options.forcePath = true
		case "-f":
			if options.file != "" || index+1 >= len(arguments) || arguments[index+1] == "" || strings.HasPrefix(arguments[index+1], "-") {
				return installOptions{}, errors.New("invalid lockfile path")
			}
			index++
			options.file = arguments[index]
		default:
			if value == "" || strings.HasPrefix(value, "-") {
				return installOptions{}, errors.New("invalid install argument")
			}
			options.apps = append(options.apps, value)
		}
	}
	if (options.file == "") == (len(options.apps) == 0) {
		return installOptions{}, errors.New("select applications or a lockfile")
	}
	return options, nil
}

func uninstallArguments(arguments []string) ([]string, error) {
	if len(arguments) == 0 {
		return nil, errors.New("applications required")
	}
	for _, value := range arguments {
		if value == "" || strings.HasPrefix(value, "-") {
			return nil, errors.New("invalid uninstall argument")
		}
	}
	return arguments, nil
}

func lockArguments(arguments []string) (string, error) {
	if len(arguments) == 0 {
		return "", nil
	}
	if len(arguments) == 2 && arguments[0] == "--output" && arguments[1] != "" && !strings.HasPrefix(arguments[1], "-") {
		return arguments[1], nil
	}
	return "", errors.New("invalid lock arguments")
}

func (r Runner) printPathConflicts(appID string, conflicts []app.PathConflict) error {
	for _, conflict := range conflicts {
		switch conflict.Type {
		case "not_in_path":
			_, err := fmt.Fprintf(r.Stderr, "Warning: %s is not in your PATH, so %s would not be runnable as a command.\n", conflict.Directory, appID)
			if err != nil {
				return err
			}
		case "shadowed":
			_, err := fmt.Fprintf(r.Stderr, "Warning: %s shadows %s; running %q would use %s instead of the TarLink-installed command.\n", conflict.Directory, appID, appID, conflict.Candidate)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func oneValueJSON(arguments []string) (string, bool, error) {
	if len(arguments) == 1 {
		value, err := oneValue(arguments)
		return value, false, err
	}
	if len(arguments) == 2 && arguments[1] == "--json" {
		value, err := oneValue(arguments[:1])
		return value, true, err
	}
	return "", false, errors.New("one value and optional --json required")
}

func onlyJSON(arguments []string) (bool, error) {
	if len(arguments) == 0 {
		return false, nil
	}
	if len(arguments) == 1 && arguments[0] == "--json" {
		return true, nil
	}
	return false, errors.New("only --json is allowed")
}

func listArguments(arguments []string) (installed, updates, jsonOutput bool, err error) {
	for _, argument := range arguments {
		switch argument {
		case "--installed":
			if installed {
				return false, false, false, errors.New("duplicate list filter")
			}
			installed = true
		case "--updates":
			if updates {
				return false, false, false, errors.New("duplicate list filter")
			}
			updates = true
		case "--json":
			if jsonOutput {
				return false, false, false, errors.New("duplicate list option")
			}
			jsonOutput = true
		default:
			return false, false, false, errors.New("unknown list option")
		}
	}
	if installed && updates {
		return false, false, false, errors.New("list filters are mutually exclusive")
	}
	return installed, updates, jsonOutput, nil
}

func title(value string) string {
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
