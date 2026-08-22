// Package cli is the thin, presentation-only TarLink command interface.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/drobilica/tarlink/internal/app"
	"github.com/drobilica/tarlink/internal/freshness"
	"github.com/drobilica/tarlink/internal/research"
	"github.com/drobilica/tarlink/internal/version"
	"go.yaml.in/yaml/v3"
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
  install      Install an application
  update       Update one or all installed applications
  rollback     Roll back an application
  uninstall    Uninstall one or all applications
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
  tarlink registry freshness <app> [--json]
  tarlink registry provenance <owner/repo> [--release <tag>] [--asset <name>] [--json] [--refresh]
  tarlink registry inspect <owner/repo> [--release <tag>] [--asset <name>] [--json] [--refresh]
  tarlink registry inspect --file <repositories.yaml> [--json]
  tarlink registry candidates [--changed] [--json]
  tarlink registry blockers [--capability <capability>] [--json]
`

var commandHelp = map[string]string{
	"refresh":     "usage: tarlink refresh",
	"list":        "usage: tarlink list [--installed|--updates] [--json]",
	"search":      "usage: tarlink search <query> [--json]",
	"install":     "usage: tarlink install <app> [--force-path]",
	"update":      "usage: tarlink update <app> | tarlink update --all",
	"self-update": "usage: tarlink self-update",
	"doctor":      "usage: tarlink doctor",
	"version":     "usage: tarlink version",
	"pin":         "usage: tarlink pin <app>",
	"unpin":       "usage: tarlink unpin <app>",
	"info":        "usage: tarlink info <app> [--json]",
	"versions":    "usage: tarlink versions <app> [--json]",
	"rollback":    "usage: tarlink rollback <app>",
	"uninstall":   "usage: tarlink uninstall <app> | tarlink uninstall --all",
}

var registryCommandHelp = map[string]string{
	"validate": "usage: tarlink registry validate <path>", "freshness": "usage: tarlink registry freshness <app> [--json]",
	"provenance": "usage: tarlink registry provenance <owner/repo> [--release <tag>] [--asset <name>] [--json] [--refresh]",
	"inspect":    "usage: tarlink registry inspect <owner/repo> [--release <tag>] [--asset <name>] [--json] [--refresh]",
	"candidates": "usage: tarlink registry candidates [--changed] [--json]", "blockers": "usage: tarlink registry blockers [--capability <capability>] [--json]",
}

type Runner struct {
	Service   app.Service
	Stdout    io.Writer
	Stderr    io.Writer
	LaunchTUI func(context.Context, app.Service, io.Writer, io.Writer) error
}

func (r Runner) Run(ctx context.Context, arguments []string) int {
	if r.Stdout == nil {
		r.Stdout = io.Discard
	}
	if r.Stderr == nil {
		r.Stderr = io.Discard
	}
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
	if r.Service == nil && arguments[0] != "version" && arguments[0] != "help" && arguments[0] != "--help" && arguments[0] != "-h" {
		return r.fail(errors.New("TarLink core is unavailable"))
	}
	if arguments[0] != "self-update" && arguments[0] != "upgrade" && arguments[0] != "doctor" && arguments[0] != "version" && arguments[0] != "help" && arguments[0] != "--help" && arguments[0] != "-h" && !contains(arguments[1:], "--json") {
		if value, checkErr := r.Service.CheckTarLinkVersion(ctx); checkErr == nil && value.UpgradeAvailable {
			_, _ = fmt.Fprintf(r.Stderr, "TarLink %s is available (current %s).\nRun `tarlink self-update` to update.\n", value.Latest, value.Current)
		}
	}

	var err error
	switch arguments[0] {
	case "registry":
		if len(arguments) == 1 || (len(arguments) == 2 && (arguments[1] == "help" || arguments[1] == "--help" || arguments[1] == "-h")) {
			_, err = io.WriteString(r.Stdout, registryHelp)
			break
		}
		if len(arguments) == 3 && arguments[1] == "validate" {
			err = r.Service.ValidateRegistry(ctx, arguments[2])
			if err == nil {
				_, err = fmt.Fprintln(r.Stdout, "Registry is valid")
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
		if len(arguments) >= 3 && (arguments[1] == "provenance" || arguments[1] == "inspect") {
			if arguments[1] == "inspect" && len(arguments) >= 4 && arguments[2] == "--file" {
				path, jsonOutput, parseErr := batchInspectArguments(arguments[3:])
				if parseErr != nil {
					return r.invalid("usage: tarlink registry inspect --file <path> [--json]")
				}
				b, readErr := os.ReadFile(path)
				if readErr != nil {
					return r.fail(readErr)
				}
				var input struct {
					Repositories []string `yaml:"repositories"`
				}
				if readErr = yaml.Unmarshal(b, &input); readErr != nil || len(input.Repositories) == 0 {
					if readErr == nil {
						readErr = errors.New("batch input requires repositories")
					}
					return r.fail(readErr)
				}
				service, ok := r.Service.(app.ResearchService)
				if !ok {
					return r.fail(errors.New("registry research is unavailable"))
				}
				results := make([]app.ResearchResult, 0, len(input.Repositories))
				for _, repo := range input.Repositories {
					v, researchErr := service.Research(ctx, app.ResearchOptions{Repository: repo, Inspect: true})
					if researchErr != nil && v.Status == "" {
						v = app.ResearchResult{Repository: research.Repository(repo), Status: "ERROR", Error: &app.ResearchError{ReasonCode: "INSPECTION_ERROR", Message: researchErr.Error()}}
					}
					results = append(results, v)
				}
				if jsonOutput {
					if e := writeJSON(r.Stdout, map[string]any{"results": results}); e != nil {
						return r.fail(e)
					}
					break
				}
				counts := map[string]int{}
				for _, v := range results {
					status := v.Status
					if status == "" {
						status = "ERROR"
					}
					counts[status]++
				}
				if _, err = fmt.Fprintf(r.Stdout, "READY_FOR_REVIEW %d\nBLOCKED %d\nERROR %d\n", counts["READY_FOR_REVIEW"], counts["BLOCKED"], counts["ERROR"]); err != nil {
					break
				}
				for _, v := range results {
					_, err = fmt.Fprintf(r.Stdout, "%-20s %-10s %s\n", v.Repository, v.Status, strings.Join(func() []string {
						if v.Inspection != nil {
							return v.Inspection.Blockers
						}
						return nil
					}(), ", "))
					if err != nil {
						return r.fail(err)
					}
				}
				break
			}
			opts, jsonOutput, parseErr := researchArguments(arguments[2:])
			if parseErr != nil {
				return r.invalid("usage: tarlink registry " + arguments[1] + " <owner/repo> [--release <tag>] [--asset <name>] [--json] [--refresh]")
			}
			opts.Inspect = arguments[1] == "inspect"
			service, ok := r.Service.(app.ResearchService)
			if !ok {
				return r.fail(errors.New("registry research is unavailable"))
			}
			value, researchErr := service.Research(ctx, opts)
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
		if arguments[1] == "candidates" {
			changed, jsonOutput, parseErr := candidateArguments(arguments[2:])
			if parseErr != nil {
				return r.invalid("usage: tarlink registry candidates [--changed] [--json]")
			}
			service, ok := r.Service.(app.CandidateService)
			if !ok {
				return r.fail(errors.New("candidate ledger is unavailable"))
			}
			if changed {
				var v research.CandidateChanges
				v, err = service.CandidateChanges(ctx)
				if err == nil {
					if jsonOutput {
						err = writeJSON(r.Stdout, v)
					} else {
						err = printChanges(r.Stdout, v)
					}
				}
			} else {
				var v research.CandidateLedger
				v, err = service.CandidateLedger()
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
			service, ok := r.Service.(app.BlockerService)
			if !ok {
				return r.fail(errors.New("blocker analysis is unavailable"))
			}
			if capability == "" {
				var v []research.BlockerSummary
				v, err = service.Blockers("")
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
				v, err = service.CapabilityPreflight(capability)
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
		return r.invalid("usage: tarlink registry <validate|freshness|provenance|inspect|candidates|blockers> ...")
	case "refresh":
		if len(arguments) != 1 {
			return r.invalid("usage: tarlink refresh")
		}
		err = r.Service.SyncRegistry(ctx, r.progress())
		if err == nil {
			_, err = io.WriteString(r.Stdout, "Application catalog refreshed.\n")
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
		value, forcePath, parseErr := installArguments(arguments[1:])
		if parseErr != nil {
			return r.invalid("usage: tarlink install <app> [--force-path]")
		}
		// PATH preflight concerns the application identity, not the requested
		// release/channel. Keep the original selector for Install below.
		selector, selectorErr := app.ParseSelector(value)
		if selectorErr != nil {
			return r.invalid("usage: tarlink install <app> [--force-path]")
		}
		conflicts, checkErr := r.Service.CheckInstallPath(selector.App)
		if checkErr != nil {
			err = checkErr
			break
		}
		if len(conflicts) != 0 && !forcePath {
			if err := r.printPathConflicts(value, conflicts); err != nil {
				return r.fail(err)
			}
			_, _ = fmt.Fprintf(r.Stderr, "Refusing to install %s because a PATH conflict was detected.\nRe-run with --force-path to acknowledge and install anyway.\n", value)
			return exitConflict
		}
		var result app.Result
		result, err = r.Service.Install(ctx, value, r.progress())
		if err == nil {
			err = r.printResult("Installed", result)
		}
	case "update":
		if len(arguments) == 2 && arguments[1] == "--all" {
			var result app.UpdateAllResult
			result, err = r.Service.UpdateAll(ctx, r.progress())
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
		result, err = r.Service.Update(ctx, value, r.progress())
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
		value, err = r.Service.UpgradeTarLink(ctx, r.progress())
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
		result, err = r.Service.Rollback(ctx, value, r.progress())
		if err == nil {
			err = r.printResult("Rolled back", result)
		}
	case "uninstall":
		if len(arguments) == 2 && arguments[1] == "--all" {
			err = r.Service.UninstallAll(ctx, r.progress())
			if err == nil {
				_, err = io.WriteString(r.Stdout, "Uninstalled all applications\n")
			}
			break
		}
		value, parseErr := oneValue(arguments[1:])
		if parseErr != nil {
			return r.invalid("usage: tarlink uninstall <app> | tarlink uninstall --all")
		}
		err = r.Service.Uninstall(ctx, value, r.progress())
		if err == nil {
			_, err = fmt.Fprintf(r.Stdout, "Uninstalled %s\n", value)
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

func (r Runner) progress() app.ProgressSink {
	last := app.ProgressStage("")
	return func(event app.Progress) {
		if event.Stage == last || event.Stage == "" {
			return
		}
		last = event.Stage
		_, _ = fmt.Fprintf(r.Stderr, "%s...\n", title(string(event.Stage)))
	}
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
	preposition := " "
	if action != "Installed" {
		preposition = " to "
	}
	if _, err := fmt.Fprintf(r.Stdout, "%s %s%s%s\n", action, result.AppID, preposition, result.Version); err != nil {
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

func installArguments(arguments []string) (string, bool, error) {
	forcePath := false
	if len(arguments) == 2 && arguments[1] == "--force-path" {
		forcePath = true
		arguments = arguments[:1]
	}
	value, err := oneValue(arguments)
	return value, forcePath, err
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
