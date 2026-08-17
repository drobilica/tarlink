// Package cli is the thin, presentation-only TarLink command interface.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/drobilica/tarlink/internal/app"
	"github.com/drobilica/tarlink/internal/version"
)

const help = `TarLink turns portable Linux application archives into managed applications.

Usage:
  tarlink registry sync
  tarlink registry validate <path>
  tarlink search <query> [--json]
  tarlink install <app>
  tarlink update <app>
  tarlink update --all
  tarlink list [--json]
  tarlink info <app> [--json]
  tarlink versions <app> [--json]
  tarlink rollback <app>
  tarlink uninstall <app>
  tarlink uninstall --all
  tarlink tui
  tarlink version
`

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
		_, _ = io.WriteString(r.Stdout, help)
		return 0
	}
	if r.Service == nil && arguments[0] != "version" && arguments[0] != "help" && arguments[0] != "--help" && arguments[0] != "-h" {
		return r.fail(errors.New("TarLink core is unavailable"))
	}

	var err error
	switch arguments[0] {
	case "registry":
		if len(arguments) == 2 && arguments[1] == "sync" {
			err = r.Service.SyncRegistry(ctx, r.progress())
			break
		}
		if len(arguments) == 3 && arguments[1] == "validate" {
			err = r.Service.ValidateRegistry(ctx, arguments[2])
			if err == nil {
				_, err = fmt.Fprintln(r.Stdout, "Registry is valid")
			}
			break
		}
		return r.invalid("usage: tarlink registry sync | tarlink registry validate <path>")
	case "search":
		value, jsonOutput, parseErr := oneValueJSON(arguments[1:])
		if parseErr != nil {
			return r.invalid("usage: tarlink search <query> [--json]")
		}
		var result []app.Application
		result, err = r.Service.Search(ctx, value)
		if err == nil {
			err = r.printApplications(result, jsonOutput)
		}
	case "install":
		value, parseErr := oneValue(arguments[1:])
		if parseErr != nil {
			return r.invalid("usage: tarlink install <app>")
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
	case "list":
		jsonOutput, parseErr := onlyJSON(arguments[1:])
		if parseErr != nil {
			return r.invalid("usage: tarlink list [--json]")
		}
		var result []app.Application
		result, err = r.Service.List(ctx)
		if err == nil {
			err = r.printApplications(result, jsonOutput)
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
	case "tui":
		if len(arguments) != 1 {
			return r.invalid("usage: tarlink tui")
		}
		if r.LaunchTUI == nil {
			err = errors.New("TUI is unavailable")
		} else {
			err = r.LaunchTUI(ctx, r.Service, r.Stdout, r.Stderr)
		}
	case "version":
		if len(arguments) != 1 {
			return r.invalid("usage: tarlink version")
		}
		_, err = fmt.Fprintf(r.Stdout, "tarlink %s\n", version.Current)
	case "help", "--help", "-h":
		if len(arguments) != 1 {
			return r.invalid("usage: tarlink help")
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

func (r Runner) printApplications(values []app.Application, jsonOutput bool) error {
	if jsonOutput {
		return writeJSON(r.Stdout, values)
	}
	if len(values) == 0 {
		_, err := io.WriteString(r.Stdout, "No applications found.\n")
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
	_, err := fmt.Fprintf(r.Stdout, "%s\n\nID:          %s\nVersion:     %s\nInstalled:   %s\nUpdate:      %s\nCategories:  %s\nHomepage:    %s\n",
		value.Name, value.ID, value.RegistryVersion, installed, update, strings.Join(value.Categories, ", "), value.Homepage)
	return err
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
		if _, err := fmt.Fprintf(r.Stdout, "No update for %s\n", id); err != nil {
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

func title(value string) string {
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}
